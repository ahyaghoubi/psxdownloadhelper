package tracker

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
)

func seen(t *Tracker, raw, title string, part int) string {
	u, _ := url.Parse(raw)
	return t.Seen(u, match.KindPKGApp, match.Hint{TitleHint: title, PartIndex: part})
}

func TestQueryStringsShareOneItem(t *testing.T) {
	tr := New()
	k1 := seen(tr, "http://h/a/x_0.pkg?sig=1", "PPSA00001", 0)
	k2 := seen(tr, "http://h/a/x_0.pkg?sig=2", "PPSA00001", 0)
	items, _ := tr.Snapshot()
	if k1 != k2 || len(items) != 1 {
		t.Fatalf("keys %q %q, %d items", k1, k2, len(items))
	}
	if items[0].URL != "http://h/a/x_0.pkg?sig=2" {
		t.Errorf("URL should be the latest one: %s", items[0].URL)
	}
}

func TestOrdering(t *testing.T) {
	tr := New()
	base := time.Now()
	tr.now = func() time.Time { return base }
	seen(tr, "http://h/old_1.pkg", "CUSA00001", 1)
	seen(tr, "http://h/old_0.pkg", "CUSA00001", 0)
	tr.now = func() time.Time { return base.Add(time.Minute) }
	seen(tr, "http://h/new_0.pkg", "PPSA00001", 0)

	items, _ := tr.Snapshot()
	got := []string{items[0].Name, items[1].Name, items[2].Name}
	want := []string{"new_0.pkg", "old_0.pkg", "old_1.pkg"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestCountersAndClear(t *testing.T) {
	tr := New()
	k := seen(tr, "http://h/x_0.pkg", "", 0)
	tr.SetSize(k, 100)
	tr.SetSize(k, 0) // ignored
	tr.AddFromSony(k, 10, 10)
	tr.AddFromLibrary(k, 20)
	tr.AddFromLibrary("unknown", 5) // no panic
	tr.ConsoleSeen("192.168.1.50:51234")
	items, c := tr.Snapshot()
	if it := items[0]; it.Size != 100 || it.FromSony != 10 || it.FromLibrary != 20 {
		t.Fatalf("item = %+v", it)
	}
	if c.Addr != "192.168.1.50" {
		t.Errorf("console addr = %q", c.Addr)
	}
	tr.Clear()
	if items, _ := tr.Snapshot(); len(items) != 0 {
		t.Fatal("clear")
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := t.TempDir() + "/sub/links.json"
	a := New()
	k := seen(a, "http://h/x_0.pkg?tok=1", "PPSA00001", 0)
	a.SetSize(k, 4000)
	u, _ := url.Parse("http://h/x_1.pkg?tok=1")
	if !a.Found(a.Generation(), u, match.KindPKGApp, "PPSA00001", 1, 3000) {
		t.Fatal("Found should add a new part")
	}
	if a.Found(a.Generation(), u, match.KindPKGApp, "PPSA00001", 1, 3000) {
		t.Fatal("Found must not add a duplicate")
	}
	if err := a.Save(path); err != nil {
		t.Fatal(err)
	}

	b := New()
	if err := b.Load(path); err != nil {
		t.Fatal(err)
	}
	items, _ := b.Snapshot()
	if len(items) != 2 || items[0].Size != 4000 || !items[0].Requested || items[1].Requested || items[1].URL != "http://h/x_1.pkg?tok=1" {
		t.Fatalf("restored %+v", items)
	}
	if err := New().Load(t.TempDir() + "/missing.json"); err != nil {
		t.Fatalf("missing file should be fine: %v", err)
	}
}

func TestFindingStatus(t *testing.T) {
	tr := New()
	_, done := tr.StartFinding("PPSA00001")
	if !tr.Finding()["PPSA00001"] {
		t.Fatal("search should be reported as running")
	}
	done()
	done() // safe to call twice
	if len(tr.Finding()) != 0 {
		t.Fatal("search should be finished")
	}
}

func TestModes(t *testing.T) {
	tr := New()
	k := seen(tr, "http://h/EP1-PPSA00001_00-GAME_0.pkg", "PPSA00001", 0)
	k2 := seen(tr, "http://h/EP1-PPSA00001_00-PATCH_3.pkg", "PPSA00001", 3)
	k3 := seen(tr, "http://h/EP1-PPSA00001_00-PATCH_4.pkg", "PPSA00001", 4)

	// Everything starts as a game, however the console reads it.
	tr.AddFromSony(k, 2<<20, 3<<30) // a PS5 reads the tail of a game's last part first
	if tr.PackageOf(k).ConsoleDownloads() || tr.PackageOf(k2).ConsoleDownloads() {
		t.Fatal("packages must default to download on PC")
	}

	// The user marks the patch as an update: all its parts follow.
	if !tr.SetMode("EP1-PPSA00001_00-PATCH", ModeConsole) {
		t.Fatal("SetMode")
	}
	if !tr.PackageOf(k2).ConsoleDownloads() || !tr.PackageOf(k3).ConsoleDownloads() {
		t.Fatal("parts of the same package must share the mode")
	}
	if tr.PackageOf(k).ConsoleDownloads() {
		t.Fatal("the base game must not be affected")
	}
	if tr.SetMode("nope", ModePC) {
		t.Fatal("unknown package")
	}

	// The choice survives a restart.
	path := t.TempDir() + "/links.json"
	if err := tr.Save(path); err != nil {
		t.Fatal(err)
	}
	tr2 := New()
	if err := tr2.Load(path); err != nil {
		t.Fatal(err)
	}
	if !tr2.PackageOf(k2).ConsoleDownloads() || tr2.PackageOf(k).ConsoleDownloads() {
		t.Fatal("modes lost on restart")
	}
}

// links.json files written by the build with automatic detection carry a
// "sparse" flag. It must be ignored, so wrongly detected games come back.
func TestOldSparseFlagIgnored(t *testing.T) {
	path := t.TempDir() + "/links.json"
	old := `{"items":[{"Key":"h/EP1-PPSA00001_00-GTA_51.pkg","URL":"http://h/EP1-PPSA00001_00-GTA_51.pkg","Name":"EP1-PPSA00001_00-GTA_51.pkg","Kind":"pkg-app","Package":"EP1-PPSA00001_00-GTA","Part":51,"Requested":true}],
"packages":{"EP1-PPSA00001_00-GTA":{"sparse":true}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := New()
	if err := tr.Load(path); err != nil {
		t.Fatal(err)
	}
	if tr.PackageOf("h/EP1-PPSA00001_00-GTA_51.pkg").ConsoleDownloads() {
		t.Fatal("old automatic 'update' guess must not survive")
	}
}

func TestClearDropsLateSearchResults(t *testing.T) {
	tr := New()
	seen(tr, "http://h/G_0.pkg", "PPSA00001", 0)
	gen, done := tr.StartFinding("PPSA00001")
	tr.Clear() // user presses Clear while the search is still running
	u, _ := url.Parse("http://h/G_1.pkg")
	if tr.Found(gen, u, match.KindPKGApp, "PPSA00001", 1, 10) {
		t.Fatal("a search started before Clear must not refill the list")
	}
	if items, _ := tr.Snapshot(); len(items) != 0 {
		t.Fatalf("list refilled: %+v", items)
	}
	if len(tr.Finding()) != 0 {
		t.Fatal("Clear should drop the searching indicator")
	}
	done()
	_, done2 := tr.StartFinding("PPSA00001")
	if !tr.Finding()["PPSA00001"] {
		t.Fatal("a new search after Clear should show")
	}
	done2()
	if !tr.Found(tr.Generation(), u, match.KindPKGApp, "PPSA00001", 1, 10) {
		t.Fatal("a search started after Clear should add parts")
	}
}

const (
	revOld = "d918060c4bd9613203567db42557b59e39928edd7c0aa20a129336913acd7ca8"
	revNew = "93c83fd4af552ecdb7f28933d802efa3cd1714f7d87cbe3a04b4618118e63952"
)

func revURL(ver, rev string) string {
	return "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01547_00/app/pkg/" + ver + "/f_" + rev +
		"/EP1004-PPSA01547_00-GRANDTHEFTAUTO00_51.pkg"
}

func TestRevision(t *testing.T) {
	if got := Revision(revURL("15", revNew)); got != revNew {
		t.Errorf("Revision = %q", got)
	}
	if got := Revision("http://h/app/pkg/1/f_" + strings.ToUpper(revNew) + "/x_0.pkg"); got != revNew {
		t.Errorf("upper-case hex: %q", got)
	}
	for _, p := range []string{"http://h/app/pkg/1/f_abc/x_0.pkg", "http://h/f_60d5/x.pkg", "http://h/x_0.pkg",
		"http://h/f_" + revNew + "0/x.pkg"} {
		if got := Revision(p); got != "" {
			t.Errorf("Revision(%q) = %q, want none", p, got)
		}
	}
}

func TestWindowAcrossRevisions(t *testing.T) {
	tr := New()
	t0 := time.Date(2026, 11, 14, 13, 46, 0, 0, time.UTC)
	t1 := time.Date(2026, 11, 15, 14, 25, 34, 0, time.UTC)
	tr.now = func() time.Time { return t0 }
	kOld := seen(tr, revURL("13", revOld), "PPSA01547", 51)
	if w := tr.Window(kOld); w != (Window{}) || !w.Fits(t0.Add(-time.Hour)) {
		t.Fatalf("one revision: any copy must fit, got %+v", w)
	}

	tr.now = func() time.Time { return t1 }
	kNew := seen(tr, revURL("15", revNew), "PPSA01547", 51)
	downloadedYesterday := t0.Add(2 * time.Hour)
	if w := tr.Window(kNew); w.Fits(downloadedYesterday) || w.Superseded() {
		t.Errorf("a copy written before the new revision appeared must not fit it: %+v", w)
	}
	if w := tr.Window(kOld); !w.Fits(downloadedYesterday) || !w.Superseded() {
		t.Errorf("the copy belongs to the old revision: %+v", w)
	}
	// Written before either revision was captured: the oldest one.
	if w := tr.Window(kOld); !w.Fits(t0.Add(-24 * time.Hour)) {
		t.Error("oldest revision should accept copies written before it was captured")
	}
	redownloaded := t1.Add(time.Hour)
	if w := tr.Window(kNew); !w.Fits(redownloaded) {
		t.Error("a fresh download must fit the new revision")
	}
	if w := tr.Window(kOld); w.Fits(redownloaded) {
		t.Error("a fresh download must not fit the old revision")
	}

	// Clear forgets the links but not that the name has two revisions.
	tr.Clear()
	tr.now = func() time.Time { return t1.Add(2 * time.Hour) }
	kNew = seen(tr, revURL("15", revNew), "PPSA01547", 51)
	if w := tr.Window(kNew); w.Fits(downloadedYesterday) {
		t.Error("after Clear, an old copy must still not fit the new revision")
	}
}

func TestRevisionsSavedAndRebuilt(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 11, 14, 13, 46, 0, 0, time.UTC)
	t1 := time.Date(2026, 11, 15, 14, 25, 34, 0, time.UTC)
	a := New()
	a.now = func() time.Time { return t0 }
	seen(a, revURL("13", revOld), "PPSA01547", 51)
	a.now = func() time.Time { return t1 }
	seen(a, revURL("15", revNew), "PPSA01547", 51)
	if err := a.Save(dir + "/links.json"); err != nil {
		t.Fatal(err)
	}

	b := New()
	if err := b.Load(dir + "/links.json"); err != nil {
		t.Fatal(err)
	}
	kNew := KeyOf(mustURL(t, revURL("15", revNew)))
	if w := b.Window(kNew); !w.From.Equal(t1) {
		t.Errorf("loaded window = %+v", w)
	}

	// links.json written before revisions were saved: rebuilt from items.
	data, _ := os.ReadFile(dir + "/links.json")
	var st map[string]any
	_ = json.Unmarshal(data, &st)
	delete(st, "revisions")
	data, _ = json.Marshal(st)
	_ = os.WriteFile(dir+"/old.json", data, 0o600)
	c := New()
	if err := c.Load(dir + "/old.json"); err != nil {
		t.Fatal(err)
	}
	if w := c.Window(kNew); !w.From.Equal(t1) {
		t.Errorf("rebuilt window = %+v", w)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
