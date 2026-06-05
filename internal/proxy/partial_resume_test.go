package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rangeUpstream serves body with ETag/Last-Modified and honours Range +
// If-Range. If the If-Range validator does not match etag it returns the full
// 200 body (mirroring real CDN behaviour for a changed object).
func rangeUpstream(t *testing.T, body []byte, etag string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		rng := r.Header.Get("Range")
		ifr := r.Header.Get("If-Range")
		if rng != "" && (ifr == "" || ifr == etag) {
			var start int64
			if _, err := fmt.Sscanf(rng, "bytes=%d-", &start); err != nil || start < 0 || start >= int64(len(body)) {
				http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(body)-1, len(body)))
			w.Header().Set("Content-Length", fmt.Sprintf("%d", int64(len(body))-start))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body[start:])
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write(body)
	}))
}

// seedPartial writes a half-finished .partial plus a matching meta sidecar
// into dir, as if a previous run had dropped mid-download.
func seedPartial(t *testing.T, dir, name, rawURL string, prefix []byte, total int64, etag string) {
	t.Helper()
	partDir := filepath.Join(dir, ".psxdh-partial")
	if err := os.MkdirAll(partDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partDir, name+".partial"), prefix, 0o644); err != nil {
		t.Fatal(err)
	}
	meta := partialMeta{URL: rawURL, Basename: name, ContentLength: total, ETag: etag}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(partDir, name+".partial.meta"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear within deadline", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPartialCacheResumesAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	body := []byte("the quick brown fox jumps over the lazy dog — resume across runs test payload")
	const etag = `"v1-abc"`
	upstream := rangeUpstream(t, body, etag)
	defer upstream.Close()

	rawURL := upstream.URL + "/resume.pkg"
	half := len(body) / 2
	seedPartial(t, dir, "resume.pkg", rawURL, body[:half], int64(len(body)), etag)

	proxySrv := makePartialProxy(t, dir, mustHost(t, upstream.URL), 0)
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("client got status %d, want 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("client got %q, want full body", got)
	}

	final := filepath.Join(dir, "resume.pkg")
	waitForFile(t, final)
	onDisk, _ := os.ReadFile(final)
	if !bytes.Equal(onDisk, body) {
		t.Errorf("on-disk body differs after resume: got %q", onDisk)
	}
	if _, err := os.Stat(filepath.Join(dir, ".psxdh-partial", "resume.pkg.partial.meta")); !os.IsNotExist(err) {
		t.Errorf("meta sidecar should be removed after promotion, err=%v", err)
	}
}

func TestPartialCacheResumeValidatorChangedFallsBackFresh(t *testing.T) {
	dir := t.TempDir()
	body := bytes.Repeat([]byte("ABCD"), 64) // 256 bytes
	upstream := rangeUpstream(t, body, `"v2-new"`)
	defer upstream.Close()

	rawURL := upstream.URL + "/changed.pkg"
	// Seed with a STALE validator: server now serves "v2-new", so If-Range
	// downgrades to a full 200 and we must redownload cleanly.
	seedPartial(t, dir, "changed.pkg", rawURL, body[:100], int64(len(body)), `"v1-stale"`)

	proxySrv := makePartialProxy(t, dir, mustHost(t, upstream.URL), 0)
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("client got %d bytes, want %d (fresh full body)", len(got), len(body))
	}

	final := filepath.Join(dir, "changed.pkg")
	waitForFile(t, final)
	onDisk, _ := os.ReadFile(final)
	if !bytes.Equal(onDisk, body) {
		t.Errorf("on-disk body after fresh fallback differs: got %d bytes", len(onDisk))
	}
}

func TestPartialCacheResumeMetaWithoutPartialIsFresh(t *testing.T) {
	dir := t.TempDir()
	body := bytes.Repeat([]byte("z"), 200)
	upstream := rangeUpstream(t, body, `"v1"`)
	defer upstream.Close()

	rawURL := upstream.URL + "/orphan.pkg"
	// Meta present but no .partial file → resumeState must yield a fresh plan.
	partDir := filepath.Join(dir, ".psxdh-partial")
	if err := os.MkdirAll(partDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := partialMeta{URL: rawURL, Basename: "orphan.pkg", ContentLength: int64(len(body)), ETag: `"v1"`}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(partDir, "orphan.pkg.partial.meta"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	proxySrv := makePartialProxy(t, dir, mustHost(t, upstream.URL), 0)
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("client got %d bytes, want %d", len(got), len(body))
	}
	waitForFile(t, filepath.Join(dir, "orphan.pkg"))
}

func TestParseContentRangeTotal(t *testing.T) {
	cases := map[string]int64{
		"bytes 100-199/200": 200,
		"bytes 0-0/1":       1,
		"bytes 5-9/*":       -1,
		"":                  -1,
		"garbage":           -1,
		"bytes 0-9/abc":     -1,
	}
	for in, want := range cases {
		if got := parseContentRangeTotal(in); got != want {
			t.Errorf("parseContentRangeTotal(%q) = %d, want %d", in, got, want)
		}
	}
}
