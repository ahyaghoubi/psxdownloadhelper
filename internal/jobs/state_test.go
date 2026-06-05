package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/cluster"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/session"
)

type stubLib struct{ have map[string]bool }

func (s stubLib) HasBasename(name string) bool { return s.have[name] }

func TestStateStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := NewStateStore(path)
	jobs := []Job{{
		Title: "PPSA01",
		Parts: []JobPart{{
			Basename: "PPSA01_0.pkg", URL: "http://cdn/PPSA01_0.pkg",
			Kind: "pkg-app", PartIndex: 0, State: PartPending,
		}},
		UpdatedAt: time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC),
	}}
	if err := store.Save(jobs); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "PPSA01" || got[0].Parts[0].State != PartPending {
		t.Fatalf("round-trip = %+v", got)
	}
}

func TestDeriveJobsMarksDoneFromLibrary(t *testing.T) {
	sessions := []session.Session{{
		Title: "PPSA01",
		Parts: []session.Part{{
			Basename: "PPSA01_0.pkg", URL: "http://cdn/PPSA01_0.pkg",
			Kind: "pkg-app", PartIndex: 0, Local: false,
		}},
	}}
	jobs := DeriveJobs(DeriveInput{
		Sessions: sessions,
		Library:  stubLib{have: map[string]bool{"PPSA01_0.pkg": true}},
	})
	if len(jobs) != 1 || jobs[0].Parts[0].State != PartDone {
		t.Fatalf("derive = %+v", jobs)
	}
}

func TestMergeJobsPrefersNewerUpdatedAt(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	a := []Job{{Title: "T1", UpdatedAt: old, Parts: []JobPart{{Basename: "old.pkg", URL: "http://x/old"}}}}
	b := []Job{{Title: "T1", UpdatedAt: newer, Parts: []JobPart{{Basename: "new.pkg", URL: "http://x/new"}}}}
	merged := MergeJobs(a, b)
	if len(merged) != 1 || merged[0].Parts[0].Basename != "new.pkg" {
		t.Fatalf("merge = %+v", merged)
	}
}

func TestResubmitPendingSkipsDone(t *testing.T) {
	mgr := cluster.NewManager(cluster.Deps{LibDir: t.TempDir()})
	jobs := []Job{{
		Title: "PPSA01",
		Parts: []JobPart{
			{Basename: "a.pkg", URL: "http://cdn/a.pkg", State: PartDone},
			{Basename: "b.pkg", URL: "http://cdn/b.pkg", State: PartPending},
		},
	}}
	ResubmitPending(jobs, mgr)
	snap := mgr.Snapshot()
	if len(snap.Games) != 1 || snap.Games[0].TotalParts != 1 {
		t.Fatalf("cluster = %+v", snap.Games)
	}
}

func TestNewStateStoreEmptyPath(t *testing.T) {
	if NewStateStore("") != nil {
		t.Error("empty path should return nil store")
	}
	if err := (*StateStore)(nil).Save(nil); err != nil {
		t.Errorf("nil save: %v", err)
	}
	_, err := (*StateStore)(nil).Load()
	if err != nil {
		t.Errorf("nil load: %v", err)
	}
}

func TestStateStoreMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	got, err := NewStateStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("missing file should yield nil, got %+v", got)
	}
}

func TestEventsFromJobsRoundTrip(t *testing.T) {
	jobs := []Job{{
		Title: "CUSA01",
		Parts: []JobPart{{
			Basename: "CUSA01_0.pkg", URL: "http://cdn/CUSA01_0.pkg",
			Kind: "pkg-app", PartIndex: 0,
		}},
	}}
	events := EventsFromJobs(jobs)
	if len(events) != 1 || events[0].Hint.TitleHint != "CUSA01" {
		t.Fatalf("events = %+v", events)
	}
}

func TestSaveCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	if err := NewStateStore(path).Save(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
