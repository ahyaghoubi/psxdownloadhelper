package jobs

import (
	"context"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/cluster"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/session"
)

type fakeProber struct {
	mu     sync.Mutex
	known  map[string]int64 // url → size
	probes int
}

func (f *fakeProber) Exists(_ context.Context, raw string) (bool, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	if size, ok := f.known[raw]; ok {
		return true, size, nil
	}
	return false, 0, nil
}

func makeEvent(t *testing.T, raw, title string, idx int, kind match.Kind) capture.Event {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return capture.Event{
		Time:   time.Now(),
		Method: "GET",
		URL:    u,
		Kind:   kind,
		Hint:   match.Hint{TitleHint: title, PartIndex: idx},
	}
}

func TestImportFromEventsRecordsSessionsAndSubmits(t *testing.T) {
	store := session.New(nil)
	mgr := cluster.NewManager(cluster.Deps{LibDir: t.TempDir()})
	prober := &fakeProber{known: map[string]int64{
		"http://cdn.example/PPSA01_0.pkg": 1024,
		"http://cdn.example/PPSA01_1.pkg": 2048,
	}}
	events := []capture.Event{
		makeEvent(t, "http://cdn.example/PPSA01_0.pkg", "PPSA01", 0, match.KindPKGApp),
	}
	res, err := ImportFromEvents(context.Background(), events, ImportOptions{
		Sessions:  store,
		Cluster:   mgr,
		Prober:    prober,
		Enumerate: true,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Titles != 1 || res.Parts != 1 || res.Enumerated != 2 {
		t.Errorf("result = %+v, want titles=1 parts=1 enumerated=2", res)
	}
	if got := store.Snapshot(); len(got) != 1 || got[0].Title != "PPSA01" {
		t.Errorf("session not loaded: %+v", got)
	}
	snap := mgr.Snapshot()
	if len(snap.Games) != 1 || snap.Games[0].TotalParts != 2 {
		t.Errorf("cluster snapshot = %+v, want 2 parts queued", snap.Games)
	}
}

func TestImportFromEventsSkipsUnknownTitles(t *testing.T) {
	mgr := cluster.NewManager(cluster.Deps{LibDir: t.TempDir()})
	events := []capture.Event{
		makeEvent(t, "http://cdn.example/foo_0.pkg", "", 0, match.KindPKGApp),
	}
	res, err := ImportFromEvents(context.Background(), events, ImportOptions{Cluster: mgr})
	if err == nil {
		t.Fatal("expected error for events without TitleHint")
	}
	if res.Titles != 0 || res.Submitted != 0 {
		t.Errorf("unknown should not be submitted, got %+v", res)
	}
	if len(mgr.Snapshot().Games) != 0 {
		t.Errorf("cluster should be empty, got %+v", mgr.Snapshot().Games)
	}
}

func TestImportFromEventsFallsBackOnEnumerateFailure(t *testing.T) {
	mgr := cluster.NewManager(cluster.Deps{LibDir: t.TempDir()})
	prober := &fakeProber{known: map[string]int64{}} // nothing exists upstream
	events := []capture.Event{
		makeEvent(t, "http://cdn.example/PPSA01_0.pkg", "PPSA01", 0, match.KindPKGApp),
	}
	res, err := ImportFromEvents(context.Background(), events, ImportOptions{
		Cluster:   mgr,
		Prober:    prober,
		Enumerate: true,
	})
	if err != nil {
		t.Fatalf("expected fallback, got %v", err)
	}
	if res.Titles != 1 || res.Submitted == 0 {
		t.Errorf("expected captured URL to be submitted on fallback, got %+v", res)
	}
}

func TestImportFromEventsNoEnumerateUsesCapturedUrls(t *testing.T) {
	mgr := cluster.NewManager(cluster.Deps{LibDir: t.TempDir()})
	events := []capture.Event{
		makeEvent(t, "http://cdn.example/PPSA01_0.pkg", "PPSA01", 0, match.KindPKGApp),
		makeEvent(t, "http://cdn.example/PPSA01_1.pkg", "PPSA01", 1, match.KindPKGApp),
	}
	res, err := ImportFromEvents(context.Background(), events, ImportOptions{Cluster: mgr})
	if err != nil {
		t.Fatal(err)
	}
	if res.Titles != 1 || res.Parts != 2 {
		t.Errorf("result = %+v", res)
	}
	if got := mgr.Snapshot().Games; len(got) != 1 || got[0].TotalParts != 2 {
		t.Errorf("expected 2 parts, got %+v", got)
	}
}
