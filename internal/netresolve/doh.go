package netresolve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DoHResolver speaks DNS-over-HTTPS (RFC 8484, POST flavour with the
// application/dns-message media type).
//
// We always POST because some Iran-friendly endpoints aggressively cache
// the GET form. POST also avoids URL-length concerns for unusual names.
type DoHResolver struct {
	// Endpoint is the full HTTPS URL of the DoH server, e.g.
	// "https://free.shecan.ir/dns-query". Required.
	Endpoint string
	// Client is the HTTP client used for the DoH POST. If nil, a sensible
	// default is constructed. The client SHOULD NOT itself use a custom
	// DNS resolver — that would create a recursive loop.
	Client *http.Client
}

// NewDoH returns a DoHResolver with a default HTTP client.
func NewDoH(endpoint string) *DoHResolver {
	return &DoHResolver{Endpoint: endpoint}
}

// LookupHost implements Resolver via DoH.
func (d *DoHResolver) LookupHost(ctx context.Context, host string) ([]string, time.Duration, error) {
	if ip := parseIPLiteral(host); ip != "" {
		return []string{ip}, 0, nil
	}
	if d.Endpoint == "" {
		return nil, 0, errors.New("netresolve/doh: empty endpoint")
	}
	name, err := dnsmessage.NewName(dnsNameOf(host))
	if err != nil {
		return nil, 0, fmt.Errorf("netresolve/doh: invalid name %q: %w", host, err)
	}

	type res struct {
		ips []string
		ttl time.Duration
		err error
	}
	ch := make(chan res, 2)
	go func() {
		ips, ttl, err := d.query(ctx, name, dnsmessage.TypeA)
		ch <- res{ips, ttl, err}
	}()
	go func() {
		ips, ttl, err := d.query(ctx, name, dnsmessage.TypeAAAA)
		ch <- res{ips, ttl, err}
	}()

	var ips []string
	var ttl time.Duration
	var firstErr error
	for i := 0; i < 2; i++ {
		r := <-ch
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		ips = append(ips, r.ips...)
		if r.ttl > 0 && (ttl == 0 || r.ttl < ttl) {
			ttl = r.ttl
		}
	}
	if len(ips) == 0 {
		if firstErr == nil {
			firstErr = &nxDomainError{host: host}
		}
		return nil, 0, firstErr
	}
	return dedupeSort(ips), ttl, nil
}

func (d *DoHResolver) query(ctx context.Context, name dnsmessage.Name, qtype dnsmessage.Type) ([]string, time.Duration, error) {
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{
			// RFC 8484 §4.1: the ID SHOULD be 0 for DoH.
			ID:               0,
			RecursionDesired: true,
		},
		Questions: []dnsmessage.Question{{
			Name:  name,
			Type:  qtype,
			Class: dnsmessage.ClassINET,
		}},
	}
	body, err := msg.Pack()
	if err != nil {
		return nil, 0, fmt.Errorf("netresolve/doh: pack: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("netresolve/doh: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	client := d.Client
	if client == nil {
		client = defaultDoHClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("netresolve/doh: POST %s: %w", d.Endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, 0, fmt.Errorf("netresolve/doh: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/dns-message") {
		return nil, 0, fmt.Errorf("netresolve/doh: unexpected content-type %q", ct)
	}

	reply, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, 0, fmt.Errorf("netresolve/doh: read body: %w", err)
	}

	var parsed dnsmessage.Message
	if err := parsed.Unpack(reply); err != nil {
		return nil, 0, fmt.Errorf("netresolve/doh: unpack: %w", err)
	}
	if parsed.RCode == dnsmessage.RCodeNameError {
		return nil, 0, &nxDomainError{host: name.String()}
	}
	if parsed.RCode != dnsmessage.RCodeSuccess {
		return nil, 0, fmt.Errorf("netresolve/doh: server returned rcode %s", parsed.RCode)
	}
	ips, ttl := extractIPs(parsed.Answers)
	if len(ips) == 0 {
		return nil, 0, errors.New("netresolve/doh: no answer records")
	}
	return ips, ttl, nil
}

// defaultDoHClient is shared by DoHResolver instances that don't bring their
// own. It is deliberately constructed with the *system* DNS resolver to
// avoid a recursive resolution loop ("to look up the DoH server's address,
// I must use the resolver I'm building").
var defaultDoHClient = &http.Client{
	Timeout: 5 * time.Second,
}
