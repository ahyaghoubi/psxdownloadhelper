package cluster

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/downloader"
)

// fakeLib is the master's library view backed by a directory: a part is "have
// it" once a file with that basename exists in dir.
type fakeLib struct{ dir string }

func (l fakeLib) HasBasename(name string) bool {
	_, err := os.Stat(filepath.Join(l.dir, name))
	return err == nil
}

// startAgent spins up a real cluster Agent (HTTP downloader) on an httptest
// server and returns its base URL.
func startAgent(t *testing.T, name, token string) string {
	t.Helper()
	a, err := NewAgent(AgentDeps{
		Name: name, Version: "test", Token: token,
		WorkDir: t.TempDir(), Engine: "http", Down: downloader.NewHTTP(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// fakeCDN serves N parts (_0..N-1) of `size` bytes each, with Range support.
func fakeCDN(t *testing.T, n, size int) (*httptest.Server, map[string][]byte) {
	t.Helper()
	bodies := make(map[string][]byte)
	for i := 0; i < n; i++ {
		bodies[fmt.Sprintf("GAME_%d.pkg", i)] = bytes.Repeat([]byte{byte('A' + i)}, size)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := filepath.Base(r.URL.Path)
		body, ok := bodies[base]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.ServeContent(w, r, base, time.Now(), bytes.NewReader(body))
	}))
	t.Cleanup(srv.Close)
	return srv, bodies
}

func driveToCompletion(t *testing.T, m *Manager, libDir string, want int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		m.Tick(context.Background())
		have := 0
		for i := 0; i < want; i++ {
			if _, err := os.Stat(filepath.Join(libDir, fmt.Sprintf("GAME_%d.pkg", i))); err == nil {
				have++
			}
		}
		if have == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d parts collected before deadline", have, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestClusterDistributesAndCollects(t *testing.T) {
	const nParts, size = 6, 4096
	cdn, bodies := fakeCDN(t, nParts, size)

	libDir := t.TempDir()
	m := NewManager(Deps{LibDir: libDir, Library: fakeLib{dir: libDir}, MaxPerNode: 2, PollInterval: 10 * time.Millisecond})

	// Two slave nodes.
	for _, name := range []string{"A", "B"} {
		if _, err := m.AddNode(context.Background(), startAgent(t, name, ""), "manual"); err != nil {
			t.Fatalf("add node %s: %v", name, err)
		}
	}

	// Enumerate parts off the fake CDN and submit them.
	parts := make([]PartURL, nParts)
	for i := 0; i < nParts; i++ {
		name := fmt.Sprintf("GAME_%d.pkg", i)
		parts[i] = PartURL{Index: i, URL: cdn.URL + "/cdn/" + name, Basename: name, Size: int64(size)}
	}
	m.Submit("GAME", parts)

	driveToCompletion(t, m, libDir, nParts)

	// Every part present in the master library with correct bytes.
	for name, want := range bodies {
		got, err := os.ReadFile(filepath.Join(libDir, name))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s bytes differ", name)
		}
	}

	snap := m.Snapshot()
	if len(snap.Nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(snap.Nodes))
	}
	if snap.DoneParts != nParts {
		t.Errorf("done parts = %d, want %d", snap.DoneParts, nParts)
	}
	if len(snap.Games) != 1 || snap.Games[0].HaveParts != nParts {
		t.Errorf("game progress = %+v", snap.Games)
	}
}

func TestClusterSkipsManuallyPresentParts(t *testing.T) {
	const nParts, size = 4, 2048
	cdn, _ := fakeCDN(t, nParts, size)
	libDir := t.TempDir()

	// Manually "drop" part 0 (as if copied via SSD) before submitting.
	if err := os.WriteFile(filepath.Join(libDir, "GAME_0.pkg"), bytes.Repeat([]byte{'A'}, size), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(Deps{LibDir: libDir, Library: fakeLib{dir: libDir}, MaxPerNode: 2, PollInterval: 10 * time.Millisecond})
	if _, err := m.AddNode(context.Background(), startAgent(t, "A", ""), "manual"); err != nil {
		t.Fatal(err)
	}

	parts := make([]PartURL, nParts)
	for i := 0; i < nParts; i++ {
		name := fmt.Sprintf("GAME_%d.pkg", i)
		parts[i] = PartURL{Index: i, URL: cdn.URL + "/cdn/" + name, Basename: name, Size: int64(size)}
	}
	m.Submit("GAME", parts)

	// Only 3 parts should be queued (part 0 already present).
	snap := m.Snapshot()
	if snap.PendingParts != nParts-1 {
		t.Errorf("pending = %d, want %d (part 0 should be skipped)", snap.PendingParts, nParts-1)
	}

	driveToCompletion(t, m, libDir, nParts)
}

func TestClusterTokenEnforced(t *testing.T) {
	url := startAgent(t, "secure", "sekret")
	// Wrong token → AddNode enlist fails.
	m := NewManager(Deps{LibDir: t.TempDir(), Token: "wrong", PollInterval: time.Second})
	if _, err := m.AddNode(context.Background(), url, "manual"); err == nil {
		t.Error("expected enlist to fail with wrong token")
	}
	// Correct token → ok.
	m2 := NewManager(Deps{LibDir: t.TempDir(), Token: "sekret", PollInterval: time.Second})
	if _, err := m2.AddNode(context.Background(), url, "manual"); err != nil {
		t.Errorf("enlist with correct token failed: %v", err)
	}
}

func TestClusterReassignsOnNodeOffline(t *testing.T) {
	const nParts, size = 3, 1024
	cdn, _ := fakeCDN(t, nParts, size)
	libDir := t.TempDir()
	m := NewManager(Deps{LibDir: libDir, Library: fakeLib{dir: libDir}, MaxPerNode: 1, PollInterval: 10 * time.Millisecond})

	// Node A will be taken offline; node B stays up.
	aURL := startAgent(t, "A", "")
	bURL := startAgent(t, "B", "")
	aID, _ := m.AddNode(context.Background(), aURL, "manual")
	m.AddNode(context.Background(), bURL, "manual")

	parts := make([]PartURL, nParts)
	for i := 0; i < nParts; i++ {
		name := fmt.Sprintf("GAME_%d.pkg", i)
		parts[i] = PartURL{Index: i, URL: cdn.URL + "/cdn/" + name, Basename: name, Size: int64(size)}
	}
	m.Submit("GAME", parts)

	// One assign cycle, then remove node A (requeues its work to B).
	m.Tick(context.Background())
	m.RemoveNode(aID)

	driveToCompletion(t, m, libDir, nParts)
}

func TestClusterMasterActsAsLocalNode(t *testing.T) {
	const nParts, size = 3, 2048
	cdn, bodies := fakeCDN(t, nParts, size)

	libDir := t.TempDir()
	m := NewManager(Deps{LibDir: libDir, Library: fakeLib{dir: libDir}, MaxPerNode: 2, PollInterval: 10 * time.Millisecond})

	// Register the master itself as a node, backed by the deterministic HTTP downloader.
	local := NewLocalNode("master", "test", "http", libDir, downloader.NewHTTP(nil))
	if _, err := m.AddLocalNode(context.Background(), local, "local"); err != nil {
		t.Fatalf("add local node: %v", err)
	}

	parts := make([]PartURL, nParts)
	for i := 0; i < nParts; i++ {
		name := fmt.Sprintf("GAME_%d.pkg", i)
		parts[i] = PartURL{Index: i, URL: cdn.URL + "/cdn/" + name, Basename: name, Size: int64(size)}
	}
	m.Submit("GAME", parts)

	driveToCompletion(t, m, libDir, nParts)

	for name, want := range bodies {
		got, err := os.ReadFile(filepath.Join(libDir, name))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s bytes differ", name)
		}
	}
}
