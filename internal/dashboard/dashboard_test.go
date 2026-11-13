package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/tracker"
)

const (
	u0 = "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01234_00/app/pkg/1/f/EP0000-PPSA01234_00-GAME_0.pkg"
	n0 = "EP0000-PPSA01234_00-GAME_0.pkg"
)

type fixture struct {
	h     *Handler
	lib   *library.Library
	track *tracker.Tracker
	dir   string
	key   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	lib, err := library.New(dir, []string{".part"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tr := tracker.New()
	u, _ := url.Parse(u0)
	key := tr.Seen(u, match.KindPKGApp, match.Hint{TitleHint: "PPSA01234", PartIndex: 0})
	return &fixture{h: New(Info{Version: "test", Port: "8080"}, lib, tr), lib: lib, track: tr, dir: dir, key: key}
}

func (f *fixture) write(t *testing.T, name string, n int) {
	t.Helper()
	p := filepath.Join(f.dir, name)
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(p, old, old)
	f.lib.Scan()
}

func (f *fixture) status(t *testing.T) Status {
	t.Helper()
	st := f.h.Build()
	if len(st.Titles) != 1 || len(st.Titles[0].Files) != 1 {
		t.Fatalf("titles = %+v", st.Titles)
	}
	return st.Titles[0].Files[0].Status
}

func TestStatusLifecycle(t *testing.T) {
	f := newFixture(t)
	if s := f.status(t); s != StatusNeeded {
		t.Fatalf("captured only: %s", s)
	}

	f.track.SetSize(f.key, 100)
	f.track.AddFromSony(f.key, 10, 10)
	if s := f.status(t); s != StatusFromSony {
		t.Fatalf("console pulling from Sony: %s", s)
	}

	f.write(t, n0+".part", 40)
	if s := f.status(t); s != StatusDownloading {
		t.Fatalf("FDM writing: %s", s)
	}

	_ = os.Remove(filepath.Join(f.dir, n0+".part"))
	f.write(t, n0, 100)
	if s := f.status(t); s != StatusReady {
		t.Fatalf("complete in library: %s", s)
	}

	f.track.AddFromLibrary(f.key, 50)
	if s := f.status(t); s != StatusSending {
		t.Fatalf("console installing: %s", s)
	}

	f.track.AddFromLibrary(f.key, 50)
	f.h.now = func() time.Time { return time.Now().Add(time.Minute) }
	if s := f.status(t); s != StatusSent {
		t.Fatalf("all bytes sent: %s", s)
	}
}

func TestSentCountsBytesFromSonyAndPC(t *testing.T) {
	f := newFixture(t)
	f.track.SetSize(f.key, 100)
	f.track.AddFromSony(f.key, 30, 30) // before the user paused the console
	f.write(t, n0, 100)
	f.track.AddFromLibrary(f.key, 70) // resumed from the PC
	f.h.now = func() time.Time { return time.Now().Add(time.Minute) }
	if s := f.status(t); s != StatusSent {
		t.Fatalf("got %s", s)
	}
}

func TestWrongSize(t *testing.T) {
	f := newFixture(t)
	f.track.SetSize(f.key, 100)
	f.write(t, n0, 99)
	if s := f.status(t); s != StatusWrongSize {
		t.Fatalf("got %s", s)
	}
}

func TestUnknownSizeStillReady(t *testing.T) {
	f := newFixture(t)
	f.write(t, n0, 77)
	st := f.h.Build()
	file := st.Titles[0].Files[0]
	if file.Status != StatusReady || file.Size != 77 {
		t.Fatalf("got %+v", file)
	}
}

func TestManifestsAreCountedNotListed(t *testing.T) {
	f := newFixture(t)
	u, _ := url.Parse("http://gst.prod.dl.playstation.net/x/app/info/PPSA01234.json")
	f.track.Seen(u, match.KindManifestJSON, match.Hint{})
	st := f.h.Build()
	if st.Other != 1 || len(st.Titles) != 1 {
		t.Fatalf("other=%d titles=%d", st.Other, len(st.Titles))
	}
}

func TestHTTPEndpoints(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	resp, err = http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if st.Port != "8080" || len(st.Titles) != 1 || st.Titles[0].ID != "PPSA01234" {
		t.Fatalf("state = %+v", st)
	}

	resp, _ = http.Get(srv.URL + "/api/urls.txt?need=1&download=1")
	if body := readBody(t, resp); strings.TrimSpace(body) != u0 || !strings.Contains(resp.Header.Get("Content-Disposition"), "psxdh-urls.txt") {
		t.Fatalf("urls.txt = %q", body)
	}

	f.write(t, n0, 10)
	resp, _ = http.Get(srv.URL + "/api/urls.txt?need=1")
	if body := readBody(t, resp); body != "" {
		t.Fatalf("file already in library should not be listed as needed: %q", body)
	}

	resp, err = http.Post(srv.URL+"/api/clear", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || len(f.h.Build().Titles) != 0 {
		t.Fatal("clear did not forget captures")
	}
	if len(f.h.Build().Library) != 1 {
		t.Fatal("clear must not touch the library")
	}

	resp, _ = http.Get(srv.URL + "/api/clear")
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("GET /api/clear = %d; only POST may clear", resp.StatusCode)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpdatesAreLeftToTheConsole(t *testing.T) {
	f := newFixture(t)
	f.track.SetSize(f.key, 80<<30)
	f.track.NoteRequest(f.key, 40<<30) // console jumps into the middle: an update

	st := f.h.Build()
	if len(st.Titles) != 1 || !st.Titles[0].Update || !st.Titles[0].Detected {
		t.Fatalf("expected an update card: %+v", st.Titles)
	}
	if s := st.Titles[0].Files[0].Status; s != StatusConsole {
		t.Fatalf("status %s", s)
	}
	if st.Counts[StatusNeeded] != 0 {
		t.Fatal("updates must not count as needing download")
	}

	srv := httptest.NewServer(f.h)
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/api/urls.txt?need=1")
	if body := readBody(t, resp); body != "" {
		t.Fatalf("update links must not be offered for download: %q", body)
	}

	// The user says: download it on the PC after all.
	var asked string
	f.h.OnDownloadOnPC = func(pkg string) { asked = pkg }
	resp, err := http.Post(srv.URL+"/api/mode", "application/json", strings.NewReader(`{"package":"EP0000-PPSA01234_00-GAME","mode":"pc"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || asked != "EP0000-PPSA01234_00-GAME" {
		t.Fatalf("mode: %d, asked %q", resp.StatusCode, asked)
	}
	if st := f.h.Build(); st.Titles[0].Update {
		t.Fatal("override to PC ignored")
	}
	resp, _ = http.Get(srv.URL + "/api/urls.txt?need=1")
	if body := readBody(t, resp); strings.TrimSpace(body) != u0 {
		t.Fatalf("link should be offered now: %q", body)
	}

	for _, bad := range []string{`{"package":"EP0000-PPSA01234_00-GAME","mode":"x"}`, `{"package":"nope","mode":"pc"}`, `garbage`} {
		resp, _ := http.Post(srv.URL+"/api/mode", "application/json", strings.NewReader(bad))
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("%s accepted", bad)
		}
	}
}
