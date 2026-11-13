package tracker

import (
	"net/url"
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
	if !a.Found(u, match.KindPKGApp, "PPSA00001", 1, 3000) {
		t.Fatal("Found should add a new part")
	}
	if a.Found(u, match.KindPKGApp, "PPSA00001", 1, 3000) {
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
	done := tr.StartFinding("PPSA00001")
	if !tr.Finding()["PPSA00001"] {
		t.Fatal("search should be reported as running")
	}
	done()
	done() // safe to call twice
	if len(tr.Finding()) != 0 {
		t.Fatal("search should be finished")
	}
}

func TestUpdateDetection(t *testing.T) {
	tr := New()
	k := seen(tr, "http://h/EP1-PPSA00001_00-GAME_0.pkg", "PPSA00001", 0)

	// A game download moves forward from the start.
	if p := tr.NoteRequest(k, 0); p.ConsoleDownloads() {
		t.Fatal("request from byte 0 is not an update")
	}
	tr.AddFromSony(k, 10<<20, 10<<20)
	if p := tr.NoteRequest(k, 10<<20); p.ConsoleDownloads() {
		t.Fatal("continuing where it left off is not an update")
	}
	if p := tr.NoteRequest(k, 30<<20); p.ConsoleDownloads() {
		t.Fatal("a small skip is not an update")
	}

	// An update jumps far into the package.
	k2 := seen(tr, "http://h/EP1-PPSA00001_00-PATCH_3.pkg", "PPSA00001", 3)
	if p := tr.NoteRequest(k2, 2<<30); !p.ConsoleDownloads() || !p.Sparse {
		t.Fatalf("jump into the middle should mark an update: %+v", p)
	}
	// Other files of the same package share the decision.
	k3 := seen(tr, "http://h/EP1-PPSA00001_00-PATCH_4.pkg", "PPSA00001", 4)
	if !tr.PackageOf(k3).ConsoleDownloads() {
		t.Fatal("parts of the same package must share the update decision")
	}
	if tr.PackageOf(k).ConsoleDownloads() {
		t.Fatal("the base game must not be affected")
	}

	// The user can override, and back.
	if !tr.SetMode("EP1-PPSA00001_00-PATCH", ModePC) || tr.PackageOf(k2).ConsoleDownloads() {
		t.Fatal("ModePC must override detection")
	}
	if !tr.SetMode("EP1-PPSA00001_00-GAME", ModeConsole) || !tr.PackageOf(k).ConsoleDownloads() {
		t.Fatal("ModeConsole must apply")
	}
	if tr.SetMode("nope", ModePC) {
		t.Fatal("unknown package")
	}

	// Decisions survive a restart.
	path := t.TempDir() + "/links.json"
	if err := tr.Save(path); err != nil {
		t.Fatal(err)
	}
	tr2 := New()
	if err := tr2.Load(path); err != nil {
		t.Fatal(err)
	}
	if !tr2.PackageOf(k).ConsoleDownloads() || tr2.PackageOf(k2).ConsoleDownloads() {
		t.Fatal("modes lost on restart")
	}
}
