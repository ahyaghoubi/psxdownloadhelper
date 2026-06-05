package captureio

import (
	"net/url"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
)

func ev(rawURL, title string, idx int, kind match.Kind) capture.Event {
	u, _ := url.Parse(rawURL)
	return capture.Event{
		URL:    u,
		Method: "GET",
		Kind:   kind,
		Hint:   match.Hint{TitleHint: title, PartIndex: idx},
		Time:   time.Now(),
	}
}

func TestAggregateByTitleDedupesAndGroups(t *testing.T) {
	events := []capture.Event{
		ev("http://cdn/PPSA01_0.pkg", "PPSA01", 0, match.KindPKGApp),
		ev("http://cdn/PPSA01_1.pkg", "PPSA01", 1, match.KindPKGApp),
		ev("http://cdn/PPSA01_0.pkg?retry=1", "PPSA01", 0, match.KindPKGApp), // dedupe by basename
		ev("http://cdn/CUSA02_0.pkg", "CUSA02", 0, match.KindPKGApp),
		ev("http://cdn/manifest.json", "PPSA01", -1, match.KindManifestJSON), // dropped
	}
	got := AggregateByTitle(events)
	if len(got) != 2 {
		t.Fatalf("want 2 titles, got %d (%v)", len(got), got)
	}
	if len(got["PPSA01"]) != 2 {
		t.Fatalf("PPSA01 parts = %d, want 2", len(got["PPSA01"]))
	}
	// Last-write-wins on dedupe; query string change should reflect.
	first := got["PPSA01"][0]
	if first.URL.RawQuery != "retry=1" {
		t.Errorf("dedupe should keep last seen URL, got %q", first.URL.String())
	}
	// Sort order: by part index ascending.
	if got["PPSA01"][0].PartIndex != 0 || got["PPSA01"][1].PartIndex != 1 {
		t.Errorf("not sorted: %+v", got["PPSA01"])
	}
}

func TestAggregateSkipsNonPushable(t *testing.T) {
	events := []capture.Event{
		ev("http://cdn/x.crc", "PPSA01", 0, match.KindCRC),
		ev("http://cdn/y.json", "PPSA01", -1, match.KindManifestJSON),
		ev("http://cdn/x_0.pkg", "PPSA01", 0, match.KindPKGApp),
	}
	got := AggregateByTitle(events)
	if len(got["PPSA01"]) != 1 || got["PPSA01"][0].Kind != match.KindPKGApp {
		t.Fatalf("non-pushable kinds should be dropped, got %+v", got)
	}
}

func TestAggregateEmptyTitleFallsBackToUnknown(t *testing.T) {
	events := []capture.Event{ev("http://cdn/anon_0.pkg", "", 0, match.KindPKGApp)}
	got := AggregateByTitle(events)
	if _, ok := got["unknown"]; !ok {
		t.Fatalf("missing 'unknown' bucket: %+v", got)
	}
}

func TestPickEnumerateSeedPrefersLowestPrimary(t *testing.T) {
	parts := []Part{
		{Basename: "x_2.pkg", URL: mustURL("http://cdn/x_2.pkg"), Kind: match.KindPKGApp, PartIndex: 2},
		{Basename: "x_0.pkg", URL: mustURL("http://cdn/x_0.pkg"), Kind: match.KindPKGApp, PartIndex: 0},
		{Basename: "x_sc.pkg", URL: mustURL("http://cdn/x_sc.pkg"), Kind: match.KindPKGSC, PartIndex: -1},
	}
	got := PickEnumerateSeed(parts)
	if got == nil || got.Path != "/x_0.pkg" {
		t.Errorf("seed = %v, want /x_0.pkg", got)
	}
}

func TestPickEnumerateSeedFallbackToSecondary(t *testing.T) {
	parts := []Part{
		{Basename: "x_sc.pkg", URL: mustURL("http://cdn/x_sc.pkg"), Kind: match.KindPKGSC, PartIndex: -1},
		{Basename: "x-DP.pkg", URL: mustURL("http://cdn/x-DP.pkg"), Kind: match.KindPKGDelta, PartIndex: -1},
	}
	got := PickEnumerateSeed(parts)
	if got == nil {
		t.Fatal("expected fallback seed, got nil")
	}
}

func TestPickEnumerateSeedEmpty(t *testing.T) {
	if got := PickEnumerateSeed(nil); got != nil {
		t.Errorf("nil parts → seed should be nil, got %v", got)
	}
}

func TestURLsForExportSortedAndFiltered(t *testing.T) {
	byTitle := map[string][]Part{
		"PPSA01": {
			{Basename: "_1.pkg", URL: mustURL("http://cdn/_1.pkg"), PartIndex: 1},
			{Basename: "_0.pkg", URL: mustURL("http://cdn/_0.pkg"), PartIndex: 0},
		},
		"CUSA02": {
			{Basename: "y_0.pkg", URL: mustURL("http://cdn/y_0.pkg"), PartIndex: 0},
		},
	}
	all := URLsForExport(byTitle, "")
	if len(all) != 3 || all[0] != "http://cdn/y_0.pkg" {
		t.Errorf("all titles: %v", all)
	}
	one := URLsForExport(byTitle, "PPSA01")
	if len(one) != 2 || one[0] != "http://cdn/_0.pkg" {
		t.Errorf("filter PPSA01: %v", one)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
