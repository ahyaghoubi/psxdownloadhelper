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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/tracker"
)

const pkgURL = "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01234_00/app/pkg/1/f_abc/EP0000-PPSA01234_00-GAME000000000000_0.pkg?token=x"
const pkgName = "EP0000-PPSA01234_00-GAME000000000000_0.pkg"

// fakeSony stands in for the PlayStation CDN. The proxy reaches it through
// the upstream-proxy setting, so real CDN host names can be used in tests.
type fakeSony struct {
	*httptest.Server
	hits atomic.Int64
	body []byte
}

func newFakeSony(t *testing.T, body []byte) *fakeSony {
	f := &fakeSony{body: body}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		switch {
		case r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, "posted:%s", b)
		case strings.HasSuffix(r.URL.Path, ".pkg"):
			http.ServeContent(w, r, "x.pkg", time.Time{}, bytes.NewReader(f.body))
		default:
			fmt.Fprint(w, "other:"+r.URL.Host+r.URL.Path)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

type harness struct {
	proxy   *httptest.Server
	client  *http.Client
	lib     *library.Library
	track   *tracker.Tracker
	libDir  string
	sony    *fakeSony
	dashHit atomic.Int64
}

func newHarness(t *testing.T, sonyBody []byte) *harness {
	t.Helper()
	return newHarnessWith(t, sonyBody, nil, true)
}

// newHarnessWith lets a test supply its own CDN handler and enable the
// background part search.
func newHarnessWith(t *testing.T, sonyBody []byte, cdn http.Handler, disableFind bool) *harness {
	t.Helper()
	h := &harness{libDir: t.TempDir(), sony: newFakeSony(t, sonyBody), track: tracker.New()}
	if cdn != nil {
		h.sony.Config.Handler = cdn
	}
	rules, err := match.LoadDefaults(true, true)
	if err != nil {
		t.Fatal(err)
	}
	h.lib, err = library.New(h.libDir, []string{".part"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	up, _ := url.Parse(h.sony.URL)
	p, err := New(Deps{
		Rules:   rules,
		Library: h.lib,
		Tracker: h.track,
		Dashboard: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			h.dashHit.Add(1)
			fmt.Fprint(w, "dashboard")
		}),
		UpstreamProxy:    up,
		DisableFindParts: disableFind,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.proxy = httptest.NewServer(p)
	t.Cleanup(h.proxy.Close)
	pu, _ := url.Parse(h.proxy.URL)
	h.client = &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test TLS server
	}}
	return h
}

func (h *harness) addLibraryFile(t *testing.T, name string, data []byte) {
	t.Helper()
	p := filepath.Join(h.libDir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(p, old, old)
	h.lib.Scan()
}

func (h *harness) get(t *testing.T, raw, rng string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, raw, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func TestForwardsAndTracksWhenNotInLibrary(t *testing.T) {
	body := payload(5000)
	h := newHarness(t, body)

	resp, got := h.get(t, pkgURL, "bytes=0-999")
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(got, body[:1000]) {
		t.Fatalf("status %d, %d bytes", resp.StatusCode, len(got))
	}
	items, console := h.track.Snapshot()
	if len(items) != 1 {
		t.Fatalf("tracked %d items", len(items))
	}
	it := items[0]
	if it.Name != pkgName || it.Title != "PPSA01234" || it.Part != 0 || it.Kind != match.KindPKGApp {
		t.Errorf("item = %+v", it)
	}
	if it.Size != 5000 {
		t.Errorf("size from Content-Range = %d, want 5000", it.Size)
	}
	if it.FromSony != 1000 || it.URL != pkgURL {
		t.Errorf("FromSony = %d, URL = %s", it.FromSony, it.URL)
	}
	if console.Addr == "" {
		t.Error("console address not recorded")
	}
}

func TestServesFromLibraryWithRange(t *testing.T) {
	body := payload(5000)
	h := newHarness(t, body)
	h.addLibraryFile(t, pkgName, body)

	resp, got := h.get(t, pkgURL, "bytes=100-199")
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(got, body[100:200]) {
		t.Fatalf("status %d, body ok=%v", resp.StatusCode, bytes.Equal(got, body[100:200]))
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 100-199/5000" {
		t.Errorf("Content-Range = %q", cr)
	}
	resp, got = h.get(t, pkgURL, "")
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("full GET status %d", resp.StatusCode)
	}
	if n := h.sony.hits.Load(); n != 0 {
		t.Errorf("Sony was contacted %d times; library hits must not go upstream", n)
	}
	items, _ := h.track.Snapshot()
	if items[0].FromLibrary != 5100 {
		t.Errorf("FromLibrary = %d, want 5100", items[0].FromLibrary)
	}
}

func TestHeadFromLibrary(t *testing.T) {
	body := payload(300)
	h := newHarness(t, body)
	h.addLibraryFile(t, pkgName, body)
	req, _ := http.NewRequest(http.MethodHead, pkgURL, nil)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength != 300 || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("HEAD: %d len=%d ranges=%q", resp.StatusCode, resp.ContentLength, resp.Header.Get("Accept-Ranges"))
	}
}

func TestWrongSizedLibraryFileIsNotServed(t *testing.T) {
	body := payload(5000)
	h := newHarness(t, body)
	h.get(t, pkgURL, "bytes=0-0") // learn the real size (5000)
	h.addLibraryFile(t, pkgName, payload(4000))

	_, got := h.get(t, pkgURL, "bytes=4500-4599")
	if !bytes.Equal(got, body[4500:4600]) {
		t.Fatal("expected bytes from Sony, not from the truncated library copy")
	}
	if h.sony.hits.Load() != 2 {
		t.Errorf("Sony hits = %d, want 2", h.sony.hits.Load())
	}
}

func TestPartialDownloadIsNotServed(t *testing.T) {
	body := payload(1000)
	h := newHarness(t, body)
	h.addLibraryFile(t, pkgName+".part", body[:10])
	_, got := h.get(t, pkgURL, "")
	if !bytes.Equal(got, body) || h.sony.hits.Load() != 1 {
		t.Fatal("an in-progress download must not be served")
	}
}

func TestUnrelatedURLWithSameNameIsNotHijacked(t *testing.T) {
	h := newHarness(t, nil)
	h.addLibraryFile(t, "index.html", []byte("local"))
	_, got := h.get(t, "http://example.com/index.html", "")
	if string(got) != "other:example.com/index.html" {
		t.Fatalf("got %q; unclassified non-PKG URLs must always be forwarded", got)
	}
}

func TestForwardsPostBody(t *testing.T) {
	h := newHarness(t, nil)
	resp, err := h.client.Post("http://example.com/api", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "posted:hello" {
		t.Fatalf("got %q", b)
	}
}

func TestDirectRequestGoesToDashboard(t *testing.T) {
	h := newHarness(t, nil)
	resp, err := http.Get(h.proxy.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "dashboard" || h.sony.hits.Load() != 0 {
		t.Fatalf("got %q", b)
	}
	if _, c := h.track.Snapshot(); c.Addr != "" {
		t.Error("a browser opening the dashboard must not count as the console")
	}
}

func TestSelfAddressedProxyRequestGoesToDashboard(t *testing.T) {
	h := newHarness(t, nil)
	rules, _ := match.LoadDefaults(true, true)
	p, _ := New(Deps{
		Rules: rules, Library: h.lib, Tracker: h.track,
		Dashboard: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "dashboard") }),
		IsSelf:    func(hp string) bool { return hp == "me:8080" },
	})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://me:8080/", nil))
	if rec.Body.String() != "dashboard" {
		t.Fatalf("got %q", rec.Body.String())
	}
}

func TestConnectTunnel(t *testing.T) {
	h := newHarness(t, nil)
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "secure")
	}))
	defer tlsSrv.Close()
	resp, got := h.get(t, tlsSrv.URL, "")
	if resp.StatusCode != 200 || string(got) != "secure" {
		t.Fatalf("CONNECT tunnel: %d %q", resp.StatusCode, got)
	}
}

func TestUpstreamUnreachableReturns502(t *testing.T) {
	h := newHarness(t, nil)
	rules, _ := match.LoadDefaults(true, true)
	p, _ := New(Deps{Rules: rules, Library: h.lib, Tracker: h.track, Dashboard: http.NotFoundHandler()})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/x", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestFullSize(t *testing.T) {
	cases := []struct {
		code int
		cl   int64
		cr   string
		want int64
	}{
		{200, 42, "", 42},
		{206, 10, "bytes 0-9/1234", 1234},
		{206, 10, "bytes 0-9/*", 0},
		{404, 10, "", 0},
	}
	for _, c := range cases {
		r := &http.Response{StatusCode: c.code, ContentLength: c.cl, Header: http.Header{}}
		if c.cr != "" {
			r.Header.Set("Content-Range", c.cr)
		}
		if got := fullSize(r); got != c.want {
			t.Errorf("fullSize(%d,%q) = %d, want %d", c.code, c.cr, got, c.want)
		}
	}
}

func TestFindsOtherPartsInBackground(t *testing.T) {
	var gotUA atomic.Value
	cdn := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.UserAgent(); ua != "" {
			gotUA.Store(ua)
		}
		for i := 0; i < 4; i++ {
			if strings.HasSuffix(r.URL.Path, fmt.Sprintf("_%d.pkg", i)) {
				http.ServeContent(w, r, "p.pkg", time.Time{}, bytes.NewReader(make([]byte, 100+i)))
				return
			}
		}
		http.NotFound(w, r)
	})
	h := newHarnessWith(t, nil, cdn, false)
	seed := strings.Replace(pkgURL, "_0.pkg", "_2.pkg", 1)
	req, _ := http.NewRequest(http.MethodGet, seed, nil)
	req.Header.Set("User-Agent", "PS5-test")
	req.Header.Set("Range", "bytes=0-0")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		items, _ := h.track.Snapshot()
		if len(items) == 4 && len(h.track.Finding()) == 0 {
			for _, it := range items {
				if it.Size != int64(100+it.Part) || it.Title != "PPSA01234" {
					t.Errorf("part %d: %+v", it.Part, it)
				}
				if it.Requested != (it.Part == 2) {
					t.Errorf("part %d Requested=%v", it.Part, it.Requested)
				}
				if !strings.HasSuffix(it.URL, "?token=x") {
					t.Errorf("part %d lost its query string: %s", it.Part, it.URL)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("found %d items, finding=%v", len(items), h.track.Finding())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if gotUA.Load() != "PS5-test" {
		t.Errorf("part search should reuse the console's User-Agent, got %v", gotUA.Load())
	}
}

// A PS5 starts a game by reading the tail of its last part. That must be
// treated as a game: every part is found.
func TestTailReadOfLastPartIsAGame(t *testing.T) {
	cdn := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i <= 3; i++ {
			if strings.HasSuffix(r.URL.Path, fmt.Sprintf("_%d.pkg", i)) {
				w.Header().Set("Content-Range", "bytes 0-0/4000000000")
				if r.Header.Get("Range") != "bytes=0-0" {
					w.Header().Set("Content-Range", "bytes 3900000000-3900000099/4000000000")
				}
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte{0})
				return
			}
		}
		http.NotFound(w, r)
	})
	h := newHarnessWith(t, nil, cdn, false)
	last := strings.Replace(pkgURL, "_0.pkg", "_3.pkg", 1)
	h.get(t, last, "bytes=3900000000-3900000099")
	deadline := time.Now().Add(5 * time.Second)
	for {
		items, _ := h.track.Snapshot()
		if len(items) == 4 && len(h.track.Finding()) == 0 {
			if h.track.PackageOf(items[0].Key).ConsoleDownloads() {
				t.Fatal("a game must not be treated as an update")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("found %d parts", len(items))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestMarkedUpdateDoesNotSearchForParts(t *testing.T) {
	var probes atomic.Int64
	cdn := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "_3.pkg") {
			probes.Add(1)
		}
		w.Header().Set("Content-Range", "bytes 100-199/80000000000")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(make([]byte, 100))
	})
	h := newHarnessWith(t, nil, cdn, true) // no search on the first request
	upd := strings.Replace(pkgURL, "_0.pkg", "_3.pkg", 1)
	h.get(t, upd, "bytes=100-199")
	items, _ := h.track.Snapshot()
	h.track.SetMode(items[0].Package, tracker.ModeConsole)

	// Same package, now marked as an update, with search enabled.
	rules, _ := match.LoadDefaults(true, true)
	up, _ := url.Parse(h.sony.URL)
	p, _ := New(Deps{Rules: rules, Library: h.lib, Tracker: h.track, Dashboard: http.NotFoundHandler(), UpstreamProxy: up})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, upd, nil)
	req.Header.Set("Range", "bytes=100-199")
	p.ServeHTTP(rec, req)
	time.Sleep(300 * time.Millisecond)
	if n := probes.Load(); n != 0 {
		t.Fatalf("part search ran for a package marked as an update (%d probes)", n)
	}
	if items, _ := h.track.Snapshot(); items[0].SonyReach != 200 {
		t.Errorf("SonyReach = %d", items[0].SonyReach)
	}
}

func TestRangeStart(t *testing.T) {
	for h, want := range map[string]int64{
		"": 0, "bytes=0-": 0, "bytes=500-999": 500, " bytes=123-": 123,
		"bytes=-500": 0, "items=5-": 0, "bytes=abc-": 0,
	} {
		if got := rangeStart(h); got != want {
			t.Errorf("rangeStart(%q) = %d, want %d", h, got, want)
		}
	}
}
