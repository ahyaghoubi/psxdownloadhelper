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
			t.Errorf("partial cache should not be used on Range requests")
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

func TestPartialCacheSkipsRangeRequests(t *testing.T) {
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
	if _, err := os.Stat(filepath.Join(dir, "rangetest.pkg")); !os.IsNotExist(err) {
		t.Errorf("partial cache should NOT promote Range responses")
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
