package upstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/bandwidth"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/circuit"
)

func TestUpstreamNewMinimal(t *testing.T) {
	c, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("nil client")
	}
}

// fixedResolver always returns a hardcoded IP for a hostname.
type fixedResolver struct {
	ip string
}

func (f fixedResolver) LookupHost(_ context.Context, _ string) ([]string, time.Duration, error) {
	return []string{f.ip}, time.Minute, nil
}

func TestUpstreamUsesCustomResolver(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	// Build a URL like http://made-up.host:PORT/ that the OS resolver
	// can't possibly resolve. The custom resolver maps it back to 127.0.0.1.
	u, _ := url.Parse(upstream.URL)
	_, port, _ := net.SplitHostPort(u.Host)

	client, err := New(Config{
		Resolver:    fixedResolver{ip: "127.0.0.1"},
		DialTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get("http://made-up.example:" + port + "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q", body)
	}
}

func TestUpstreamPreferIPv4(t *testing.T) {
	d := &resolvingDialer{preferIPv4: true}
	ips := d.maybeFilterIPv4([]string{"2001:db8::1", "1.2.3.4", "2001:db8::2"})
	if ips[0] != "1.2.3.4" {
		t.Errorf("ipv4 not first: %v", ips)
	}
	if len(ips) != 3 {
		t.Errorf("ipv6 dropped: %v", ips)
	}
}

func TestUpstreamHostFilterMatchesSuffix(t *testing.T) {
	f := hostFilter{suffixes: []string{"prod.dl.playstation.net"}}
	if !f.matches("gst.prod.dl.playstation.net") {
		t.Error("should match subdomain")
	}
	if !f.matches("prod.dl.playstation.net") {
		t.Error("should match exact")
	}
	if f.matches("evil.com") {
		t.Error("should not match unrelated")
	}
}

func TestUpstreamHostFilterEmptyMatchesAll(t *testing.T) {
	f := hostFilter{}
	if !f.matches("anything.com") {
		t.Error("empty filter should match anything")
	}
}

func TestUpstreamProxyURLValidation(t *testing.T) {
	_, err := New(Config{UpstreamProxy: "ftp://nope.example"})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("expected unsupported scheme error, got %v", err)
	}

	_, err = New(Config{UpstreamProxy: "://broken"})
	if err == nil {
		t.Error("expected parse error")
	}

	_, err = New(Config{UpstreamProxy: "bare-string"})
	if err == nil {
		t.Errorf("expected error from scheme-less proxy URL, got nil")
	}
}

func TestUpstreamHTTPProxyConfigured(t *testing.T) {
	// A fake "proxy" server that pretends to accept absolute-URI GETs.
	called := false
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxySrv.Close()

	client, err := New(Config{
		UpstreamProxy: proxySrv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Use a target the OS resolver can satisfy (the proxy gets the
	// absolute URL anyway).
	resp, err := client.Get("http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !called {
		t.Error("HTTP proxy was not invoked")
	}
	if string(body) != "proxied" {
		t.Errorf("body = %q", body)
	}
}

func TestUpstreamCircuitBreakerBlocksFurtherDials(t *testing.T) {
	b := circuit.New(circuit.Config{FailureThreshold: 1, Cooldown: time.Minute})
	// Open the breaker for the loopback host.
	release, _ := b.Allow("notreal.example")
	release(errors.New("seed failure"))

	client, err := New(Config{
		Resolver: fixedResolver{ip: "127.0.0.1"},
		Breaker:  b,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 9 is reserved as the Discard port; even if dial succeeded, the
	// breaker should have refused it first.
	_, err = client.Get("http://notreal.example:9/")
	if err == nil {
		t.Fatal("expected breaker error")
	}
}

func TestBandwidthRoundTripperThrottlesBody(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 8*1024)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "8192")
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	bucket := bandwidth.NewBucket(8*1024, 1024) // 8 KB/s
	client, err := New(Config{Bandwidth: bucket})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	resp, err := client.Get(upstream.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	elapsed := time.Since(start)

	if len(got) != len(body) {
		t.Fatalf("body len = %d, want %d", len(got), len(body))
	}
	// 8 KB at 8 KB/s with 1 KB burst ≈ 875ms. Be generous in the lower
	// bound to avoid flakiness.
	if elapsed < 500*time.Millisecond {
		t.Errorf("download too fast: %v", elapsed)
	}
}
