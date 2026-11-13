package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/serve"
)

// makePartialProxy builds a Server with partial cache enabled and the
// library pointed at dir. It uses an override rule that classifies every
// upstream path as pkg-app, so the cache fires reliably.
func makePartialProxy(t *testing.T, dir string, upHost string, minSize int64) *httptest.Server {
	t.Helper()
	cfg := defaultCfg()
	cfg.Library.Dir = dir
	cfg.Forward.PartialCache.Enabled = true
	cfg.Forward.PartialCache.MinSizeBytes = minSize

	ruleDir := t.TempDir()
	body := fmt.Sprintf("platform: test\nrules:\n  - kind: pkg-app\n    host_suffix: %s\n    path_regex: \".*\"\n", upHost)
	if err := os.WriteFile(filepath.Join(ruleDir, "r.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rules, err := match.LoadOverride(ruleDir)
	if err != nil {
		t.Fatal(err)
	}
	bus := capture.NewBus(8)
	s, err := New(Deps{
		Config: cfg, Rules: rules, Resolver: stubResolver{}, Serve: serve.New(nil), Bus: bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestPartialCachePromotesSuccessfulDownload(t *testing.T) {
	dir := t.TempDir()
	const body = "the quick brown fox jumps over the lazy dog (partial cache test)"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			t.Errorf("fresh full download should not request an upstream Range")
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	proxySrv := makePartialProxy(t, dir, mustHost(t, upstream.URL), 0)
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/sample.pkg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if string(got) != body {
		t.Errorf("client got %q", got)
	}

	// Wait for the deferred rename to complete.
	deadline := time.Now().Add(2 * time.Second)
	final := filepath.Join(dir, "sample.pkg")
	for {
		if _, err := os.Stat(final); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sample.pkg was not promoted into library dir within deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}

	onDisk, _ := os.ReadFile(final)
	if !bytes.Equal(onDisk, []byte(body)) {
		t.Errorf("on-disk body differs: got %q", onDisk)
	}

	if _, err := os.Stat(filepath.Join(dir, ".psxdh-partial", "sample.pkg.partial")); !os.IsNotExist(err) {
		t.Errorf(".partial should be removed after promotion, got err=%v", err)
	}
}

func TestPartialCacheCachesIncompleteRangeRequests(t *testing.T) {
	dir := t.TempDir()
	body := bytes.Repeat([]byte("x"), 64)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f.bin", time.Now(), bytes.NewReader(body))
	}))
	defer upstream.Close()

	proxySrv := makePartialProxy(t, dir, mustHost(t, upstream.URL), 0)
	client := proxiedClient(t, proxySrv)

	req, _ := http.NewRequest(http.MethodGet, upstream.URL+"/rangetest.pkg", nil)
	req.Header.Set("Range", "bytes=0-15")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	time.Sleep(150 * time.Millisecond)
	meta, err := readMeta(filepath.Join(dir, ".psxdh-partial", "rangetest.pkg.partial.meta"))
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || len(meta.Spans) != 1 || meta.Spans[0] != (span{Start: 0, End: 16}) {
		t.Fatalf("range response should be cached as span [0,16), got %+v", meta)
	}
	if _, err := os.Stat(filepath.Join(dir, "rangetest.pkg")); !os.IsNotExist(err) {
		t.Errorf("partial cache should not promote incomplete Range coverage")
	}
}

func TestPartialCacheRangeRequestsPromoteWhenCoverageComplete(t *testing.T) {
	dir := t.TempDir()
	body := []byte("0123456789abcdef")
	upstream := arbitraryRangeUpstream(t, body, `"v1"`)
	defer upstream.Close()

	proxySrv := makePartialProxy(t, dir, mustHost(t, upstream.URL), 0)
	client := proxiedClient(t, proxySrv)
	rawURL := upstream.URL + "/out-of-order.pkg"

	for _, rng := range []string{"bytes=8-15", "bytes=0-3", "bytes=4-7"} {
		resp := doRange(t, client, rawURL, rng)
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("%s status = %d, want 206", rng, resp.StatusCode)
		}
	}

	final := filepath.Join(dir, "out-of-order.pkg")
	waitForFile(t, final)
	onDisk, _ := os.ReadFile(final)
	if !bytes.Equal(onDisk, body) {
		t.Fatalf("promoted range cache differs: got %q want %q", onDisk, body)
	}
}

func TestPartialCacheOverlappingRangesAreCoalesced(t *testing.T) {
	dir := t.TempDir()
	body := []byte("0123456789abcdef")
	upstream := arbitraryRangeUpstream(t, body, `"v1"`)
	defer upstream.Close()

	proxySrv := makePartialProxy(t, dir, mustHost(t, upstream.URL), 0)
	client := proxiedClient(t, proxySrv)
	rawURL := upstream.URL + "/overlap.pkg"

	for _, rng := range []string{"bytes=0-7", "bytes=4-15"} {
		resp := doRange(t, client, rawURL, rng)
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	final := filepath.Join(dir, "overlap.pkg")
	waitForFile(t, final)
	onDisk, _ := os.ReadFile(final)
	if !bytes.Equal(onDisk, body) {
		t.Fatalf("promoted overlap cache differs: got %q want %q", onDisk, body)
	}
}

func TestPartialCacheRangeValidatorMismatchStartsFresh(t *testing.T) {
	dir := t.TempDir()
	oldBody := []byte("aaaaaaaaaaaaaaaa")
	newBody := []byte("0123456789abcdef")
	rawURL := ""
	upstream := arbitraryRangeUpstream(t, newBody, `"v2"`)
	defer upstream.Close()
	rawURL = upstream.URL + "/changed-range.pkg"
	seedPartial(t, dir, "changed-range.pkg", rawURL, oldBody[:4], int64(len(oldBody)), `"v1"`)

	proxySrv := makePartialProxy(t, dir, mustHost(t, upstream.URL), 0)
	client := proxiedClient(t, proxySrv)

	resp := doRange(t, client, rawURL, "bytes=4-7")
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, newBody[4:8]) {
		t.Fatalf("client got %q, want %q", got, newBody[4:8])
	}

	meta, err := readMeta(filepath.Join(dir, ".psxdh-partial", "changed-range.pkg.partial.meta"))
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.ETag != `"v2"` || len(meta.Spans) != 1 || meta.Spans[0] != (span{Start: 4, End: 8}) {
		t.Fatalf("stale range cache should be discarded and restarted, got %+v", meta)
	}
}

func TestPartialCacheInterruptedRangeDoesNotMarkCoverage(t *testing.T) {
	dir := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-9/10")
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("01234"))
	}))
	defer upstream.Close()

	proxySrv := makePartialProxy(t, dir, mustHost(t, upstream.URL), 0)
	client := proxiedClient(t, proxySrv)

	resp := doRange(t, client, upstream.URL+"/interrupted.pkg", "bytes=0-9")
	_, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr == nil {
		t.Fatal("expected truncated range body to fail")
	}

	time.Sleep(150 * time.Millisecond)
	meta, err := readMeta(filepath.Join(dir, ".psxdh-partial", "interrupted.pkg.partial.meta"))
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil {
		t.Fatal("expected metadata to remain for interrupted range")
	}
	if len(meta.Spans) != 0 {
		t.Fatalf("interrupted range must not mark coverage, got %+v", meta.Spans)
	}
}

func TestPartialCacheRangePreservesClient206Semantics(t *testing.T) {
	dir := t.TempDir()
	body := []byte("0123456789abcdef")
	upstream := arbitraryRangeUpstream(t, body, `"v1"`)
	defer upstream.Close()

	proxySrv := makePartialProxy(t, dir, mustHost(t, upstream.URL), 0)
	client := proxiedClient(t, proxySrv)

	resp := doRange(t, client, upstream.URL+"/semantics.pkg", "bytes=5-9")
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if gotCR := resp.Header.Get("Content-Range"); gotCR != "bytes 5-9/16" {
		t.Fatalf("Content-Range = %q, want bytes 5-9/16", gotCR)
	}
	if gotCL := resp.Header.Get("Content-Length"); gotCL != "5" {
		t.Fatalf("Content-Length = %q, want 5", gotCL)
	}
	if !bytes.Equal(got, body[5:10]) {
		t.Fatalf("body = %q, want %q", got, body[5:10])
	}
}

func TestPartialCachePromotionUpdatesIndexImmediately(t *testing.T) {
	dir := t.TempDir()
	body := []byte("0123456789abcdef")
	upstream := arbitraryRangeUpstream(t, body, `"v1"`)
	defer upstream.Close()

	idx, err := library.NewIndex(dir, library.LayoutBasename)
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultCfg()
	cfg.Library.Dir = dir
	cfg.Forward.PartialCache.Enabled = true
	cfg.Forward.PartialCache.MinSizeBytes = 0

	ruleDir := t.TempDir()
	ruleBody := fmt.Sprintf("platform: test\nrules:\n  - kind: pkg-app\n    host_suffix: %s\n    path_regex: \".*\"\n", mustHost(t, upstream.URL))
	if err := os.WriteFile(filepath.Join(ruleDir, "r.yaml"), []byte(ruleBody), 0o644); err != nil {
		t.Fatal(err)
	}
	rules, err := match.LoadOverride(ruleDir)
	if err != nil {
		t.Fatal(err)
	}
	bus := capture.NewBus(8)
	s, err := New(Deps{
		Config: cfg, Rules: rules, Resolver: idx, Serve: serve.New(nil), Bus: bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(s.Handler())
	defer proxySrv.Close()
	client := proxiedClient(t, proxySrv)

	for _, rng := range []string{"bytes=0-7", "bytes=8-15"} {
		resp := doRange(t, client, upstream.URL+"/indexed.pkg", rng)
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	if path, ok := idx.Resolve(mustParseURL(t, upstream.URL+"/indexed.pkg")); !ok || filepath.Base(path) != "indexed.pkg" {
		t.Fatalf("promoted file should be indexed immediately, got path=%q ok=%v", path, ok)
	}
}

func TestPartialCacheRespectsMinSize(t *testing.T) {
	dir := t.TempDir()
	body := []byte("tiny")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	proxySrv := makePartialProxy(t, dir, mustHost(t, upstream.URL), 1024)
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/small.pkg")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "small.pkg")); !os.IsNotExist(err) {
		t.Errorf("partial cache should ignore responses below MinSizeBytes")
	}
}

func TestPartialCacheNotConfiguredIsNoOp(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultCfg()
	cfg.Library.Dir = dir
	// PartialCache.Enabled is false by default.
	rules, _ := match.LoadDefaults(true, true)
	bus := capture.NewBus(8)
	s, err := New(Deps{
		Config: cfg, Rules: rules, Resolver: stubResolver{}, Serve: serve.New(nil), Bus: bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.pcache != nil {
		t.Errorf("pcache should be nil when disabled, got %+v", s.pcache)
	}
}

func TestPartialCacheBasenameExtraction(t *testing.T) {
	cases := map[string]string{
		"/foo.pkg":                "foo.pkg",
		"/cdn/path/CUSA12345.pkg": "CUSA12345.pkg",
		"/":                       "",
		"":                        "",
		"/.":                      "",
		"/..":                     "",
		"/foo/":                   "foo",
	}
	for in, want := range cases {
		u := &url.URL{Path: in}
		if got := basenameFromURL(u); got != want {
			t.Errorf("basenameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func arbitraryRangeUpstream(t *testing.T, body []byte, etag string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		if rng := r.Header.Get("Range"); rng != "" {
			var start, end int64
			if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err != nil ||
				start < 0 || end < start || end >= int64(len(body)) {
				http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
			w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body[start : end+1])
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write(body)
	}))
}

func doRange(t *testing.T, client *http.Client, rawURL, rng string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", rng)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
