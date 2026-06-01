// Package doctor implements the diagnostic checks that back the
// `psxdh doctor` and `psxdh probe` CLI commands. The package is pure
// logic — it does not touch stdout; callers receive structured Reports
// and decide how to render them.
//
// See docs/network-resilience.md (Diagnostics) for the user-facing
// description.
package doctor

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/netresolve"
)

// DefaultPSNHosts are the CDN hosts psxdh expects to talk to. The list
// is kept small on purpose — the goal is "do I have working
// connectivity to Sony's CDN?", not "let me brute-force every PSN
// endpoint". Add to this when the rule packs grow.
var DefaultPSNHosts = []string{
	"gst.prod.dl.playstation.net",
	"sgst.prod.dl.playstation.net",
	"gs2.ww.prod.dl.playstation.net",
	"gs2-sec.ww.prod.dl.playstation.net",
}

// Report is the result of a `doctor` run.
type Report struct {
	Resolvers []ResolverResult
	Hosts     []HostResult
}

// ResolverResult bundles the outcome of a single DNS resolver probing
// every configured host.
type ResolverResult struct {
	Label   string
	Lookups []LookupResult
	Err     string // non-empty when the resolver couldn't be constructed at all
}

// LookupResult is a single host-via-one-resolver outcome.
type LookupResult struct {
	Host    string
	IPs     []string
	Latency time.Duration
	Err     string
}

// HostResult records a direct TCP+TLS handshake to host:443 using the
// stdlib resolver. It is the "is the network even reachable?" baseline
// against which the per-resolver Lookups are interpreted.
type HostResult struct {
	Host    string
	Latency time.Duration
	TLSOK   bool
	Err     string
}

// CheckOptions controls how a Doctor run behaves.
type CheckOptions struct {
	// Hosts to probe. Empty means DefaultPSNHosts.
	Hosts []string
	// HandshakeTimeout caps each TCP+TLS attempt. Zero means 5s.
	HandshakeTimeout time.Duration
	// SkipHandshake disables the TLS check (useful in CI environments
	// with no internet).
	SkipHandshake bool
}

// Check runs the full diagnostic suite against cfg.Network. The
// returned Report has at least one entry per configured resolver, plus
// the optional direct-handshake check per host.
func Check(ctx context.Context, cfg config.NetworkConfig, opts CheckOptions) *Report {
	hosts := opts.Hosts
	if len(hosts) == 0 {
		hosts = DefaultPSNHosts
	}
	hsTimeout := opts.HandshakeTimeout
	if hsTimeout <= 0 {
		hsTimeout = 5 * time.Second
	}

	rep := &Report{}
	for _, lbl := range describeResolvers(cfg.DNS) {
		rep.Resolvers = append(rep.Resolvers, runResolverProbe(ctx, lbl, hosts))
	}
	if !opts.SkipHandshake {
		for _, h := range hosts {
			rep.Hosts = append(rep.Hosts, runHostProbe(ctx, h, hsTimeout))
		}
	}
	return rep
}

// describeResolvers expands cfg into one "labelled" resolver per
// configured entry, plus a system fallback when the user opted into a
// non-system mode. The labels are user-facing — they appear verbatim in
// `psxdh doctor` output.
func describeResolvers(d config.DNSConfig) []labelledResolver {
	mode := strings.ToLower(strings.TrimSpace(d.Mode))
	if mode == "" {
		mode = "system"
	}
	var out []labelledResolver
	switch mode {
	case "system":
		out = append(out, labelledResolver{
			label: "system",
			r:     netresolve.NewSystem(),
		})
	case "udp":
		for _, r := range d.Resolvers {
			if r = strings.TrimSpace(r); r != "" {
				out = append(out, labelledResolver{label: "udp://" + r, r: netresolve.NewUDP(r)})
			}
		}
		out = append(out, labelledResolver{label: "system", r: netresolve.NewSystem()})
	case "doh":
		for _, r := range d.Resolvers {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if !strings.HasPrefix(strings.ToLower(r), "https://") {
				out = append(out, labelledResolver{label: r, err: "doh entries must start with https://"})
				continue
			}
			out = append(out, labelledResolver{label: r, r: netresolve.NewDoH(r)})
		}
		out = append(out, labelledResolver{label: "system", r: netresolve.NewSystem()})
	case "doh+udp":
		for _, r := range d.Resolvers {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(r), "https://") {
				out = append(out, labelledResolver{label: r, r: netresolve.NewDoH(r)})
			} else {
				out = append(out, labelledResolver{label: "udp://" + r, r: netresolve.NewUDP(r)})
			}
		}
		out = append(out, labelledResolver{label: "system", r: netresolve.NewSystem()})
	default:
		out = append(out, labelledResolver{label: mode, err: "unknown dns mode"})
	}
	return out
}

type labelledResolver struct {
	label string
	r     netresolve.Resolver
	err   string
}

func runResolverProbe(ctx context.Context, lbl labelledResolver, hosts []string) ResolverResult {
	rr := ResolverResult{Label: lbl.label, Err: lbl.err}
	if rr.Err != "" {
		return rr
	}
	for _, h := range hosts {
		start := time.Now()
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ips, _, err := lbl.r.LookupHost(probeCtx, h)
		cancel()
		out := LookupResult{Host: h, Latency: time.Since(start)}
		if err != nil {
			out.Err = err.Error()
		} else {
			// Sort for stable diff'able output.
			sort.Strings(ips)
			out.IPs = ips
		}
		rr.Lookups = append(rr.Lookups, out)
	}
	return rr
}

func runHostProbe(ctx context.Context, host string, timeout time.Duration) HostResult {
	start := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: timeout},
		Config: &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		},
	}
	conn, err := d.DialContext(probeCtx, "tcp", host+":443")
	res := HostResult{Host: host, Latency: time.Since(start)}
	if err != nil {
		res.Err = err.Error()
		return res
	}
	defer conn.Close()
	res.TLSOK = true
	return res
}

// ProbeResult is the structured form of `psxdh probe <url>`.
type ProbeResult struct {
	URL            string
	Kind           match.Kind
	Hint           match.Hint
	Resolved       []string
	ResolveErr     string
	ResolveLatency time.Duration
	Method         string
	Status         int
	Server         string
	ContentLength  int64
	AcceptRanges   string
	ContentType    string
	Location       string
	Headers        http.Header
	HTTPErr        string
	HTTPLatency    time.Duration
}

// Probe runs the diagnostic for a single URL: classify against the
// rule pack, resolve, then issue an HTTP HEAD (preferring GET with a
// 0-1 Range when the server doesn't support HEAD on PKG paths) and
// surface the resulting status / headers.
func Probe(ctx context.Context, target string, rules *match.RuleSet, resolver netresolve.Resolver, client *http.Client) (*ProbeResult, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("doctor: parse url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("doctor: probe requires absolute URL with scheme and host")
	}
	res := &ProbeResult{URL: u.String()}
	res.Kind, res.Hint = rules.Classify(u)

	host, _ := splitHostPort(u.Host)
	start := time.Now()
	resCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	ips, _, err := resolver.LookupHost(resCtx, host)
	cancel()
	res.ResolveLatency = time.Since(start)
	if err != nil {
		res.ResolveErr = err.Error()
	} else {
		sort.Strings(ips)
		res.Resolved = ips
	}

	// Issue a HEAD; some PSN endpoints answer 405 to HEAD on chunked
	// downloads, so fall back to a tiny GET with Range: bytes=0-0.
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return res, fmt.Errorf("doctor: new request: %w", err)
	}
	res.Method = http.MethodHead
	startHTTP := time.Now()
	resp, err := client.Do(req)
	if err == nil && resp != nil && resp.StatusCode == http.StatusMethodNotAllowed {
		_ = resp.Body.Close()
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		req.Header.Set("Range", "bytes=0-0")
		res.Method = http.MethodGet
		startHTTP = time.Now()
		resp, err = client.Do(req)
	}
	res.HTTPLatency = time.Since(startHTTP)
	if err != nil {
		res.HTTPErr = err.Error()
		return res, nil
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode
	res.Server = resp.Header.Get("Server")
	res.ContentLength = resp.ContentLength
	res.AcceptRanges = resp.Header.Get("Accept-Ranges")
	res.ContentType = resp.Header.Get("Content-Type")
	res.Location = resp.Header.Get("Location")
	res.Headers = resp.Header.Clone()
	return res, nil
}

func splitHostPort(hostport string) (string, string) {
	h, p, err := net.SplitHostPort(hostport)
	if err == nil {
		return h, p
	}
	return hostport, ""
}
