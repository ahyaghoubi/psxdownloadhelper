// Package upstream assembles the *http.Client that the proxy uses to
// fetch bytes from Sony's CDN. Everything resilience-related — custom
// DNS, IPv4 preference, dial timeouts, upstream proxy chain, circuit
// breaker, bandwidth limit — is plumbed here.
//
// The proxy never touches netresolve / circuit / bandwidth directly;
// it consumes whatever this package returns. That keeps the proxy
// focused on protocol concerns (Range, hop-by-hop headers, CONNECT)
// and the resilience knobs in one auditable place.
package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/bandwidth"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/circuit"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/netresolve"
)

// Config bundles every knob the upstream client honours. Each field is
// optional; the zero value of Config produces a vanilla net/http client.
type Config struct {
	// Resolver, if non-nil, is used for all upstream hostname lookups.
	// nil falls back to net.DefaultResolver behaviour.
	Resolver netresolve.Resolver
	// PreferIPv4 makes the dialer try IPv4 addresses first. Useful where
	// the ISP's IPv6 path to PSN is broken (common in Iran).
	PreferIPv4 bool
	// DialTimeout caps a single dial attempt. Zero defaults to 10s.
	DialTimeout time.Duration
	// UpstreamProxy is a fully-formed URL (http://, https://, or socks5://)
	// pointing at the user's VPN / proxy. Empty disables.
	UpstreamProxy string
	// UpstreamProxyOnlyForHosts, if non-empty, limits proxy use to the
	// listed host suffixes. Other hosts dial directly.
	UpstreamProxyOnlyForHosts []string
	// Breaker, if non-nil, gates outbound dials per host.
	Breaker *circuit.Breaker
	// Bandwidth, if non-nil, throttles response body reads.
	Bandwidth *bandwidth.Bucket
}

// New builds an *http.Client backed by a Transport that honours cfg.
// The returned client never follows redirects (a 30x from Sony must
// reach the console so it can re-resolve).
func New(cfg Config) (*http.Client, error) {
	dialTimeout := cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 10 * time.Second
	}

	dialer := &resolvingDialer{
		resolver:    cfg.Resolver,
		preferIPv4:  cfg.PreferIPv4,
		dialTimeout: dialTimeout,
		breaker:     cfg.Breaker,
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		DisableCompression:    true,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}

	if cfg.UpstreamProxy != "" {
		if err := installUpstreamProxy(transport, dialer, cfg); err != nil {
			return nil, fmt.Errorf("upstream: install proxy: %w", err)
		}
	}

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		// No client-level timeout: PKG transfers can run for hours.
	}

	if cfg.Bandwidth != nil && cfg.Bandwidth.Rate() > 0 {
		// Wrap each response body in a LimitedReader. We do this via a
		// RoundTripper wrapper rather than touching every call site.
		client.Transport = &bandwidthRoundTripper{base: transport, bucket: cfg.Bandwidth}
	}

	return client, nil
}

// resolvingDialer is a net.Dialer-compatible struct that runs every dial
// through (a) the user-supplied DNS resolver and (b) the optional
// per-host circuit breaker.
type resolvingDialer struct {
	resolver    netresolve.Resolver
	preferIPv4  bool
	dialTimeout time.Duration
	breaker     *circuit.Breaker

	// proxyDial, if non-nil, replaces the direct net.Dialer for hosts
	// matching proxyHostFilter.
	proxyDial       func(ctx context.Context, network, address string) (net.Conn, error)
	proxyHostFilter hostFilter
}

func (d *resolvingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		host = address
		port = ""
	}

	if d.proxyDial != nil && d.proxyHostFilter.matches(host) {
		release := func(error) {}
		if d.breaker != nil {
			var berr error
			release, berr = d.breaker.Allow(host)
			if berr != nil {
				return nil, berr
			}
		}
		conn, err := d.proxyDial(ctx, network, address)
		release(err)
		return conn, err
	}

	release := func(error) {}
	if d.breaker != nil {
		var berr error
		release, berr = d.breaker.Allow(host)
		if berr != nil {
			return nil, berr
		}
	}

	ips, err := d.resolve(ctx, host)
	if err != nil {
		release(err)
		return nil, err
	}

	addrs := make([]string, 0, len(ips))
	for _, ip := range ips {
		if port == "" {
			addrs = append(addrs, ip)
		} else {
			addrs = append(addrs, net.JoinHostPort(ip, port))
		}
	}

	conn, derr := d.dialFirst(ctx, network, addrs)
	release(derr)
	return conn, derr
}

// resolve runs host through the configured resolver. If no resolver is
// configured we delegate to the OS, exactly like the stdlib default.
func (d *resolvingDialer) resolve(ctx context.Context, host string) ([]string, error) {
	if host == "" {
		return nil, errors.New("upstream: empty host")
	}
	if d.resolver == nil {
		ips, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return nil, err
		}
		return d.maybeFilterIPv4(ips), nil
	}
	ips, _, err := d.resolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	return d.maybeFilterIPv4(ips), nil
}

func (d *resolvingDialer) maybeFilterIPv4(ips []string) []string {
	if !d.preferIPv4 {
		return ips
	}
	var v4, v6 []string
	for _, ip := range ips {
		if strings.Contains(ip, ":") {
			v6 = append(v6, ip)
		} else {
			v4 = append(v4, ip)
		}
	}
	if len(v4) > 0 {
		return append(v4, v6...) // v4 first, v6 still as fallback
	}
	return ips
}

// dialFirst tries each address in turn and returns the first success.
// The per-dial timeout is d.dialTimeout; the overall budget is ctx.
func (d *resolvingDialer) dialFirst(ctx context.Context, network string, addrs []string) (net.Conn, error) {
	if len(addrs) == 0 {
		return nil, errors.New("upstream: no addresses to dial")
	}
	var lastErr error
	for _, a := range addrs {
		dialCtx, cancel := context.WithTimeout(ctx, d.dialTimeout)
		var nd net.Dialer
		conn, err := nd.DialContext(dialCtx, network, a)
		cancel()
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("upstream: all %d addresses failed: %w", len(addrs), lastErr)
}

// hostFilter implements a "match either nothing-in-list or a suffix" rule.
type hostFilter struct{ suffixes []string }

func (f hostFilter) matches(host string) bool {
	if len(f.suffixes) == 0 {
		return true
	}
	host = stripPort(host)
	for _, s := range f.suffixes {
		s = strings.ToLower(stripPort(s))
		h := strings.ToLower(host)
		if h == s || strings.HasSuffix(h, "."+s) {
			return true
		}
	}
	return false
}

func stripPort(s string) string {
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

// installUpstreamProxy wires the configured upstream proxy (HTTP or
// SOCKS5) into transport / dialer.
func installUpstreamProxy(t *http.Transport, d *resolvingDialer, cfg Config) error {
	u, err := url.Parse(cfg.UpstreamProxy)
	if err != nil {
		return fmt.Errorf("parse upstream proxy %q: %w", cfg.UpstreamProxy, err)
	}
	scheme := strings.ToLower(u.Scheme)
	d.proxyHostFilter = hostFilter{suffixes: cfg.UpstreamProxyOnlyForHosts}

	switch scheme {
	case "http", "https":
		t.Proxy = func(req *http.Request) (*url.URL, error) {
			if d.proxyHostFilter.matches(req.URL.Host) {
				return u, nil
			}
			return nil, nil
		}
		return nil
	case "socks5", "socks5h":
		// Build a SOCKS5 dialer; route only matching hosts through it.
		auth := socksAuth(u)
		dialer, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
		if err != nil {
			return fmt.Errorf("socks5 init %s: %w", u.Host, err)
		}
		ctxDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return errors.New("upstream: socks5 dialer does not implement ContextDialer")
		}
		d.proxyDial = ctxDialer.DialContext
		return nil
	case "":
		return fmt.Errorf("upstream proxy %q lacks scheme (try http:// or socks5://)", cfg.UpstreamProxy)
	default:
		return fmt.Errorf("upstream: unsupported proxy scheme %q (want http, https, socks5)", scheme)
	}
}

func socksAuth(u *url.URL) *proxy.Auth {
	if u.User == nil {
		return nil
	}
	pwd, _ := u.User.Password()
	return &proxy.Auth{User: u.User.Username(), Password: pwd}
}

// bandwidthRoundTripper wraps an http.RoundTripper and throttles every
// response body it returns. Headers and connection setup are not
// throttled — only payload bytes.
type bandwidthRoundTripper struct {
	base   http.RoundTripper
	bucket *bandwidth.Bucket
}

func (b *bandwidthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := b.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if resp != nil && resp.Body != nil {
		resp.Body = &bandwidthBody{
			ReadCloser: resp.Body,
			limited:    bandwidth.NewLimitedReader(req.Context(), resp.Body, b.bucket, 64*1024),
		}
	}
	return resp, nil
}

type bandwidthBody struct {
	io.ReadCloser
	limited io.Reader
}

func (b *bandwidthBody) Read(p []byte) (int, error) {
	return b.limited.Read(p)
}
