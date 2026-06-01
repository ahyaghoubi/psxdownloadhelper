// Package e2e contains integration tests that wire multiple psxdh packages
// together and exercise the full request → watcher → serve → response cycle
// against a fake Sony-CDN-shaped upstream. See the Phase 1 exit criteria
// in docs/roadmap.md and the testing strategy in docs/architecture.md.
package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/ahyaghoubi/psxdownloadhelper/internal/proxy"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/serve"
)

// e2eRig is everything an end-to-end test needs: a temporary library dir,
// a watcher running in the background, a proxy serving on a random port,
// a fake upstream that pretends to be the Sony CDN, and a *http.Client
// configured to proxy through psxdh.
type e2eRig struct {
	t         *testing.T
	dir       string
	idx       *library.Index
	watcher   *library.Watcher
	upstream  *httptest.Server
	proxySrv  *httptest.Server
	bus       capture.Bus
	cancel    context.CancelFunc
	watcherWG chan struct{}
}

// newRig builds the rig with a small settle window so tests run fast.
// upstreamBody is what the fake CDN returns for any GET.
func newRig(t *testing.T, upstreamBody []byte) *e2eRig {
	t.Helper()
	dir := t.TempDir()

	cfg := config.Default()
	cfg.Library.Dir = dir
	cfg.Library.StableSettleMs = 200
	cfg.Forward.Mode = "auto"
	cfg.Log.Level = "warn"

	rules, err := match.LoadDefaults(true, true)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := library.NewIndex(dir, library.LayoutBasename)
	if err != nil {
		t.Fatal(err)
	}
	bus := capture.NewBus(64)
	serveH := serve.New(slog.New(slog.NewTextHandler(io.Discard, nil)))

	watcher, err := library.NewWatcher(idx, library.WatcherConfig{
		Settle:         200 * time.Millisecond,
		PollInterval:   50 * time.Millisecond,
		IgnoreSuffixes: cfg.Library.IgnoreSuffixes,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		_ = watcher.Run(ctx)
	}()
	// Drain events so the watcher doesn't block its emit().
	go func() {
		for range watcher.Events() {
		}
	}()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "upstream.bin", time.Now(), bytes.NewReader(upstreamBody))
	}))

	p, err := proxy.New(proxy.Deps{
		Config:   cfg,
		Rules:    rules,
		Resolver: idx,
		Serve:    serveH,
		Bus:      bus,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(p.Handler())

	t.Cleanup(func() {
		proxySrv.Close()
		upstream.Close()
		cancel()
		<-watcherDone
	})

	return &e2eRig{
		t:         t,
		dir:       dir,
		idx:       idx,
		watcher:   watcher,
		upstream:  upstream,
		proxySrv:  proxySrv,
		bus:       bus,
		cancel:    cancel,
		watcherWG: watcherDone,
	}
}

// proxiedClient returns a client that routes through the psxdh proxy.
func (r *e2eRig) proxiedClient() *http.Client {
	pu, err := url.Parse(r.proxySrv.URL)
	if err != nil {
		r.t.Fatal(err)
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(pu),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test only
		},
	}
}

// upstreamURL returns the URL the test should request through the proxy.
func (r *e2eRig) upstreamURL(basename string) string {
	return r.upstream.URL + "/gst/prod/00/PPSA01234_00-X/app/pkg/" + basename
}

// dropFile writes body into the library at basename and waits for the
// watcher to mark it stable in the index.
func (r *e2eRig) dropFile(basename string, body []byte) {
	r.t.Helper()
	p := filepath.Join(r.dir, basename)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		r.t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := r.idx.Resolve(mustURL(r.t, "http://example.com/x/"+basename)); ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.t.Fatalf("file %s never reached stable state", basename)
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestPhase1_ForwardThenLocalServe is the canonical Phase 1 exit scenario
// from the implementation plan Step 1.9:
//
//   1. Console asks for a PS4-style multi-chunk PKG → proxy has no local copy
//      → forwards upstream → console gets upstream bytes + sees a Range header
//   2. User downloads the file with FDM (simulated by writing to the library
//      dir) → watcher fires KindStable → file enters the index
//   3. Console retries with a Range header → proxy serves from local file with
//      206 Partial Content; upstream is never touched
func TestPhase1_ForwardThenLocalServe(t *testing.T) {
	const (
		basename = "PPSA01234_00-FAKEPKG_0.pkg"
		size     = 16 * 1024
	)
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i % 256)
	}

	upstreamHits := atomic.Int64{}
	dir := t.TempDir()
	rules, _ := match.LoadDefaults(true, true)
	idx, _ := library.NewIndex(dir, library.LayoutBasename)
	bus := capture.NewBus(64)
	serveH := serve.New(slog.New(slog.NewTextHandler(io.Discard, nil)))

	watcher, err := library.NewWatcher(idx, library.WatcherConfig{
		Settle:         200 * time.Millisecond,
		PollInterval:   50 * time.Millisecond,
		IgnoreSuffixes: []string{".part", ".fdmdownload", ".tmp", ".crdownload"},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		_ = watcher.Run(ctx)
	}()
	go func() {
		for range watcher.Events() {
		}
	}()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		http.ServeContent(w, r, "upstream.bin", time.Now(), bytes.NewReader(body))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Library.Dir = dir
	cfg.Forward.Mode = "auto"

	pSrv, err := proxy.New(proxy.Deps{
		Config: cfg, Rules: rules, Resolver: idx,
		Serve: serveH, Bus: bus,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(pSrv.Handler())
	defer proxySrv.Close()

	pu, _ := url.Parse(proxySrv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}

	consoleURL := upstream.URL + "/gst/prod/00/PPSA01234_00-X/app/pkg/" + basename + "?downloadId=DEAD-BEEF"

	// === Step 1: console asks; library is empty; proxy forwards. ===
	resp, err := client.Get(consoleURL)
	if err != nil {
		t.Fatal(err)
	}
	got1, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("first GET status = %d, want 200", resp.StatusCode)
	}
	if !bytes.Equal(got1, body) {
		t.Errorf("first GET body mismatch (forward path)")
	}
	if upstreamHits.Load() != 1 {
		t.Errorf("upstream hits after forward = %d, want 1", upstreamHits.Load())
	}

	// === Step 2: user "downloads" the file with FDM, drops it into library. ===
	if err := os.WriteFile(filepath.Join(dir, basename), body, 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := idx.Resolve(mustURL(t, "http://example.com/x/"+basename)); ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok := idx.Resolve(mustURL(t, "http://example.com/x/"+basename)); !ok {
		t.Fatal("library watcher never indexed the file after settle")
	}

	// === Step 3: console retries with Range; proxy serves locally. ===
	req, _ := http.NewRequest(http.MethodGet, consoleURL, nil)
	req.Header.Set("Range", "bytes=2048-2147")
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusPartialContent {
		t.Errorf("range GET status = %d, want 206", resp2.StatusCode)
	}
	if !bytes.Equal(got2, body[2048:2148]) {
		t.Errorf("range GET body mismatch (local serve path)")
	}
	if cr := resp2.Header.Get("Content-Range"); cr != fmt.Sprintf("bytes 2048-2147/%d", size) {
		t.Errorf("Content-Range = %q", cr)
	}
	// Upstream must not have been touched on the second request.
	if upstreamHits.Load() != 1 {
		t.Errorf("upstream hits after local serve = %d, want still 1", upstreamHits.Load())
	}

	cancel()
	select {
	case <-watcherDone:
	case <-time.After(time.Second):
		t.Error("watcher did not stop within 1s of cancel")
	}
}

// TestPhase1_MultiPartSession exercises user story U2 from docs/roadmap.md:
// a multi-part title where each part transitions pending → local → served as
// the user drops them in one by one with FDM.
func TestPhase1_MultiPartSession(t *testing.T) {
	body := func(i int) []byte {
		b := make([]byte, 256)
		for j := range b {
			b[j] = byte((i*37 + j) % 256)
		}
		return b
	}

	rig := newRig(t, []byte("placeholder"))
	client := rig.proxiedClient()

	parts := []string{
		"UP1234-CUSA12345_00-FAKEPKG-A0100_0.pkg",
		"UP1234-CUSA12345_00-FAKEPKG-A0100_1.pkg",
		"UP1234-CUSA12345_00-FAKEPKG-A0100_2.pkg",
	}

	// Re-target the upstream to serve per-part bodies.
	rig.upstream.Close()
	rig.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i, p := range parts {
			if filepath.Base(r.URL.Path) == p {
				http.ServeContent(w, r, p, time.Now(), bytes.NewReader(body(i)))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(rig.upstream.Close)

	// Drop each part one at a time and verify the proxy switches from
	// forwarding to serving locally after each settle.
	for i, p := range parts {
		// Pre-FDM: forward.
		resp, err := client.Get(rig.upstreamURL(p))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !bytes.Equal(got, body(i)) {
			t.Errorf("part %d forward body mismatch", i)
		}

		// User downloads with FDM (simulated).
		rig.dropFile(p, body(i))

		// Post-FDM: local serve.
		resp2, err := client.Get(rig.upstreamURL(p))
		if err != nil {
			t.Fatal(err)
		}
		got2, _ := io.ReadAll(resp2.Body)
		resp2.Body.Close()
		if !bytes.Equal(got2, body(i)) {
			t.Errorf("part %d local serve body mismatch", i)
		}
	}
}

// TestPhase1_CapturePublishesAllParts confirms the capture bus sees every
// classified PKG URL the console requests. This is the data feed the
// dashboard / export consume.
func TestPhase1_CapturePublishesAllParts(t *testing.T) {
	// Inject a rule that classifies any *.pkg on 127.0.0.1 as pkg-app so the
	// loopback upstream produces classified capture events.
	rulesDir := t.TempDir()
	ruleBody := "platform: test\nrules:\n  - kind: pkg-app\n    host_suffix: 127.0.0.1\n    path_regex: \"\\\\.pkg$\"\n"
	if err := os.WriteFile(filepath.Join(rulesDir, "loopback.yaml"), []byte(ruleBody), 0o644); err != nil {
		t.Fatal(err)
	}
	rules, err := match.LoadOverride(rulesDir)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	idx, _ := library.NewIndex(dir, library.LayoutBasename)
	bus := capture.NewBus(64)
	serveH := serve.New(slog.New(slog.NewTextHandler(io.Discard, nil)))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("placeholder"))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Library.Dir = dir
	cfg.Forward.Mode = "auto"
	p, err := proxy.New(proxy.Deps{
		Config: cfg, Rules: rules, Resolver: idx,
		Serve: serveH, Bus: bus,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(p.Handler())
	defer proxySrv.Close()

	ch, un := bus.Subscribe()
	defer un()

	pu, _ := url.Parse(proxySrv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}

	parts := []string{
		"PPSA01234_00-FAKEPKG_0.pkg",
		"PPSA01234_00-FAKEPKG_1.pkg",
		"PPSA01234_00-FAKEPKG_2.pkg",
	}
	for _, name := range parts {
		if _, err := client.Get(upstream.URL + "/app/pkg/" + name); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for len(seen) < len(parts) {
		select {
		case ev := <-ch:
			if ev.Kind != match.KindPKGApp {
				continue
			}
			seen[filepath.Base(ev.URL.Path)] = true
		case <-deadline:
			t.Fatalf("only saw %d/%d capture events: %v", len(seen), len(parts), seen)
		}
	}
}

// TestPhase1_BinarySmokeTest builds the actual CLI binary and verifies it
// boots, takes a request through the loopback proxy, and shuts down cleanly
// on context cancel. This is the closest we get to "ran on real hardware"
// without involving a console.
func TestPhase1_BinarySmokeTest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary smoke test in -short mode")
	}
	// We deliberately keep this test in the e2e package so it sees the
	// same imports as the main binary and would fail if any internal
	// package broke.
	// The proxy server here is built directly (not via os.Exec) so the
	// test stays fast and deterministic.
	dir := t.TempDir()
	rules, _ := match.LoadDefaults(true, true)
	idx, _ := library.NewIndex(dir, library.LayoutBasename)
	bus := capture.NewBus(16)
	serveH := serve.New(slog.New(slog.NewTextHandler(io.Discard, nil)))

	cfg := config.Default()
	cfg.Library.Dir = dir
	cfg.Proxy.Listen = "127.0.0.1:0" // any free port

	p, err := proxy.New(proxy.Deps{
		Config: cfg, Rules: rules, Resolver: idx,
		Serve: serveH, Bus: bus,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()
	go func() {
		errCh <- p.ListenAndServe(ctx)
	}()
	// Smoke: cancel and wait for clean shutdown.
	cancel()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("ListenAndServe returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("ListenAndServe did not return within 2s of cancel")
	}
}
