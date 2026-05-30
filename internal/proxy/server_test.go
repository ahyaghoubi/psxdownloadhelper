package proxy

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/serve"
)

// stubResolver always misses; used when we want to force forwarding.
type stubResolver struct{}

func (stubResolver) Resolve(*url.URL) (string, bool) { return "", false }

// fixedResolver always returns the same path for any URL.
type fixedResolver struct{ path string }

func (f fixedResolver) Resolve(*url.URL) (string, bool) { return f.path, true }

// helper: builds a Server with mode=auto and stub resolver pointed at upstream.
func makeProxy(t *testing.T, cfg *config.Config, res library.Resolver) (*Server, *httptest.Server) {
	t.Helper()
	rules, err := match.LoadDefaults(true, true)
	if err != nil {
		t.Fatal(err)
	}
	bus := capture.NewBus(64)
	s, err := New(Deps{
		Config:   cfg,
		Rules:    rules,
		Resolver: res,
		Serve:    serve.New(nil),
		Bus:      bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

func defaultCfg() *config.Config {
	c := config.Default()
	return c
}

// proxiedClient builds an *http.Client that routes through proxySrv.
func proxiedClient(t *testing.T, proxySrv *httptest.Server) *http.Client {
	t.Helper()
	pu, err := url.Parse(proxySrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(pu),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test only
		},
	}
}

func TestForwardHTTPGet(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello from upstream"))
	}))
	defer upstream.Close()

	_, proxySrv := makeProxy(t, defaultCfg(), stubResolver{})
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from upstream" {
		t.Errorf("body = %q", body)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestForwardPreservesQueryString(t *testing.T) {
	var seenQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.RawQuery
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	_, proxySrv := makeProxy(t, defaultCfg(), stubResolver{})
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/x?downloadId=DEAD-BEEF&du=1&q=z")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seenQuery != "downloadId=DEAD-BEEF&du=1&q=z" {
		t.Errorf("upstream saw query %q", seenQuery)
	}
}

func TestForwardPreservesRangeHeader(t *testing.T) {
	body := make([]byte, 1024)
	for i := range body {
		body[i] = byte(i % 256)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f.bin", time.Now(), bytes.NewReader(body))
	}))
	defer upstream.Close()

	_, proxySrv := makeProxy(t, defaultCfg(), stubResolver{})
	client := proxiedClient(t, proxySrv)

	req, _ := http.NewRequest(http.MethodGet, upstream.URL+"/f.bin", nil)
	req.Header.Set("Range", "bytes=100-199")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("status = %d, want 206", resp.StatusCode)
	}
	if !bytes.Equal(got, body[100:200]) {
		t.Errorf("range body mismatch")
	}
}

func TestLibraryHitServesLocallyWithoutForwarding(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "local.pkg")
	if err := os.WriteFile(target, []byte("local-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	upstreamHit := atomic.Bool{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHit.Store(true)
		_, _ = w.Write([]byte("upstream-bytes"))
	}))
	defer upstream.Close()

	_, proxySrv := makeProxy(t, defaultCfg(), fixedResolver{path: target})
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/local.pkg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if string(body) != "local-bytes" {
		t.Errorf("body = %q, want local-bytes", body)
	}
	if upstreamHit.Load() {
		t.Error("library hit should not reach upstream")
	}
}

func TestStrictModeBlocksUnmatched(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("should never see this"))
	}))
	defer upstream.Close()

	cfg := defaultCfg()
	cfg.Forward.Mode = "strict"
	_, proxySrv := makeProxy(t, cfg, stubResolver{})
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/foo.pkg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestCacheModeBlocksUnclassified(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("should never see this"))
	}))
	defer upstream.Close()

	cfg := defaultCfg()
	cfg.Forward.Mode = "cache"
	_, proxySrv := makeProxy(t, cfg, stubResolver{})
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/unclassified.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 for unclassified URL", resp.StatusCode)
	}
}

func TestCacheModeForwardsClassified(t *testing.T) {
	upstreamHit := atomic.Bool{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHit.Store(true)
		_, _ = w.Write([]byte("classified body"))
	}))
	defer upstream.Close()

	// Build a rule that classifies anything from the upstream host as pkg-app.
	rules, _ := match.LoadDefaults(false, false)
	// Use the override mechanism to inject a synthetic rule.
	dir := t.TempDir()
	upHost := mustHost(t, upstream.URL)
	body := fmt.Sprintf("platform: test\nrules:\n  - kind: pkg-app\n    host_suffix: %s\n    path_regex: \"\\\\.pkg$\"\n", upHost)
	if err := os.WriteFile(filepath.Join(dir, "test.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	overrideRules, err := match.LoadOverride(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = rules

	cfg := defaultCfg()
	cfg.Forward.Mode = "cache"
	bus := capture.NewBus(8)
	s, err := New(Deps{
		Config: cfg, Rules: overrideRules, Resolver: stubResolver{},
		Serve: serve.New(nil), Bus: bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(s.Handler())
	defer proxySrv.Close()

	client := proxiedClient(t, proxySrv)
	resp, err := client.Get(upstream.URL + "/classified.pkg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if string(got) != "classified body" {
		t.Errorf("body = %q", got)
	}
	if !upstreamHit.Load() {
		t.Error("classified URL should be forwarded in cache mode")
	}
}

func TestCapturePublishesClassifiedEvents(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	rules, _ := match.LoadDefaults(false, false)
	// Inject a rule that classifies any upstream path as pkg-app.
	dir := t.TempDir()
	upHost := mustHost(t, upstream.URL)
	body := fmt.Sprintf("platform: test\nrules:\n  - kind: pkg-app\n    host_suffix: %s\n    path_regex: \".*\"\n", upHost)
	_ = os.WriteFile(filepath.Join(dir, "test.yaml"), []byte(body), 0o644)
	overrideRules, _ := match.LoadOverride(dir)
	_ = rules

	bus := capture.NewBus(16)
	ch, un := bus.Subscribe()
	defer un()

	cfg := defaultCfg()
	s, _ := New(Deps{
		Config: cfg, Rules: overrideRules, Resolver: stubResolver{},
		Serve: serve.New(nil), Bus: bus,
	})
	proxySrv := httptest.NewServer(s.Handler())
	defer proxySrv.Close()

	client := proxiedClient(t, proxySrv)
	_, err := client.Get(upstream.URL + "/captured.pkg")
	if err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.Kind != match.KindPKGApp {
			t.Errorf("captured kind = %q, want pkg-app", ev.Kind)
		}
		if ev.URL.Path != "/captured.pkg" {
			t.Errorf("captured path = %q", ev.URL.Path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected capture event")
	}
}

func TestConnectTunnelsToTLSUpstream(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("https-body"))
	}))
	defer upstream.Close()

	_, proxySrv := makeProxy(t, defaultCfg(), stubResolver{})
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/whatever")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "https-body" {
		t.Errorf("tunnelled body = %q", body)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	_, proxySrv := makeProxy(t, defaultCfg(), stubResolver{})
	client := proxiedClient(t, proxySrv)

	req, _ := http.NewRequest(http.MethodPost, "http://example.com/x", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestStripHopByHop(t *testing.T) {
	h := http.Header{
		"Connection":      []string{"close, X-Custom-Hop"},
		"Keep-Alive":      []string{"timeout=5"},
		"X-Custom-Hop":    []string{"should be removed"},
		"X-Custom-Stable": []string{"should remain"},
		"Range":           []string{"bytes=0-100"},
	}
	stripHopByHop(h)
	if got := h.Get("Connection"); got != "" {
		t.Errorf("Connection should be removed, got %q", got)
	}
	if got := h.Get("Keep-Alive"); got != "" {
		t.Errorf("Keep-Alive should be removed, got %q", got)
	}
	if got := h.Get("X-Custom-Hop"); got != "" {
		t.Errorf("Connection-listed header X-Custom-Hop should be removed, got %q", got)
	}
	if got := h.Get("X-Custom-Stable"); got != "should remain" {
		t.Errorf("non-hop header was incorrectly removed, got %q", got)
	}
	if got := h.Get("Range"); got != "bytes=0-100" {
		t.Errorf("Range must be preserved, got %q", got)
	}
}

func TestNewRejectsMissingDeps(t *testing.T) {
	cfg := defaultCfg()
	rules, _ := match.LoadDefaults(true, true)
	serveH := serve.New(nil)
	bus := capture.NewBus(4)

	cases := []struct {
		name string
		d    Deps
	}{
		{"nil config", Deps{Rules: rules, Resolver: stubResolver{}, Serve: serveH, Bus: bus}},
		{"nil rules", Deps{Config: cfg, Resolver: stubResolver{}, Serve: serveH, Bus: bus}},
		{"nil resolver", Deps{Config: cfg, Rules: rules, Serve: serveH, Bus: bus}},
		{"nil serve", Deps{Config: cfg, Rules: rules, Resolver: stubResolver{}, Bus: bus}},
		{"nil bus", Deps{Config: cfg, Rules: rules, Resolver: stubResolver{}, Serve: serveH}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.d); err == nil {
				t.Error("expected error for missing dep")
			}
		})
	}
}

// mustHost extracts host:port from a URL string for use as a HostSuffix.
func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}
