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
	tr.AddFromSony(k, 10)
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
