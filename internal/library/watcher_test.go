package library

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testWatcher returns a Watcher with short timings suitable for tests.
func testWatcher(t *testing.T) (*Watcher, *Index, string) {
	t.Helper()
	dir := t.TempDir()
	idx, err := NewIndex(dir, LayoutBasename)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(idx, WatcherConfig{
		Settle:         200 * time.Millisecond,
		PollInterval:   50 * time.Millisecond,
		IgnoreSuffixes: []string{".part", ".fdmdownload", ".tmp", ".crdownload"},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return w, idx, dir
}

func waitForEvent(t *testing.T, ch <-chan Event, kind EventKind, basename string, timeout time.Duration) Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("events channel closed while waiting for %s/%s", kind, basename)
			}
			if ev.Kind == kind && (basename == "" || ev.Basename == basename) {
				return ev
			}
		case <-deadline:
			t.Fatalf("timeout waiting for %s event on %s", kind, basename)
			return Event{}
		}
	}
}

func TestWatcherEmitsStableAfterSettle(t *testing.T) {
	w, idx, dir := testWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	target := filepath.Join(dir, "small.pkg")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := waitForEvent(t, w.Events(), KindStable, "small.pkg", 2*time.Second)
	if ev.Size != 5 {
		t.Errorf("stable size = %d, want 5", ev.Size)
	}

	if got, _ := idx.Stats(); got != 1 {
		t.Errorf("index should hold 1 file after stable, got %d", got)
	}

	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned: %v", err)
	}
}

func TestWatcherRestartsSettleOnChunkedWrite(t *testing.T) {
	w, idx, dir := testWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	target := filepath.Join(dir, "chunked.pkg")
	f, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 4*1024)
	for i := 0; i < 6; i++ {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
		_ = f.Sync()
		time.Sleep(70 * time.Millisecond) // shorter than settle (200ms)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	ev := waitForEvent(t, w.Events(), KindStable, "chunked.pkg", 2*time.Second)
	if ev.Size != int64(6*len(chunk)) {
		t.Errorf("stable size = %d, want %d", ev.Size, 6*len(chunk))
	}

	if got, _ := idx.Stats(); got != 1 {
		t.Errorf("index should hold 1 file after stable, got %d", got)
	}
}

func TestWatcherIgnoresInProgressSuffixes(t *testing.T) {
	w, idx, dir := testWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Drop a .fdmdownload temp file then rename to the real name.
	tmp := filepath.Join(dir, "renamed.pkg.fdmdownload")
	final := filepath.Join(dir, "renamed.pkg")
	if err := os.WriteFile(tmp, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Give the watcher a moment to receive (and ignore) the temp event.
	time.Sleep(100 * time.Millisecond)
	if err := os.Rename(tmp, final); err != nil {
		t.Fatal(err)
	}

	ev := waitForEvent(t, w.Events(), KindStable, "renamed.pkg", 2*time.Second)
	if ev.Size != 2 {
		t.Errorf("stable size = %d, want 2", ev.Size)
	}

	// The .fdmdownload file should not appear in the index.
	all := idx.All()
	if _, ok := all["renamed.pkg.fdmdownload"]; ok {
		t.Errorf(".fdmdownload file leaked into index")
	}
	if _, ok := all["renamed.pkg"]; !ok {
		t.Errorf("renamed.pkg missing from index")
	}
}

func TestWatcherIgnoresPartialCacheDirectory(t *testing.T) {
	w, idx, dir := testWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	partDir := filepath.Join(dir, ".psxdh-partial")
	if err := os.MkdirAll(partDir, 0o755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(partDir, "game.pkg.partial")
	if err := os.WriteFile(part, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := part + ".meta"
	if err := os.WriteFile(meta, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-w.Events():
		t.Fatalf("partial cache internals should not emit library events, got %+v", ev)
	case <-time.After(350 * time.Millisecond):
	}
	if got, _ := idx.Stats(); got != 0 {
		t.Fatalf("partial cache internals should not be indexed, got %d files", got)
	}
}

func TestWatcherWaitsForAria2SidecarRemoval(t *testing.T) {
	w, idx, dir := testWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	target := filepath.Join(dir, "aria2.pkg")
	sidecar := target + ".aria2"
	if err := os.WriteFile(target, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, []byte("state"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-w.Events():
		if ev.Kind == KindStable && ev.Basename == "aria2.pkg" {
			t.Fatal("aria2 target became stable while sidecar existed")
		}
	case <-time.After(350 * time.Millisecond):
	}
	if _, ok := idx.Resolve(mustParse(t, "http://example.com/aria2.pkg")); ok {
		t.Fatal("aria2 target should not be indexed while sidecar exists")
	}

	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	ev := waitForEvent(t, w.Events(), KindStable, "aria2.pkg", 2*time.Second)
	if ev.Size != int64(len("partial")) {
		t.Errorf("stable size = %d", ev.Size)
	}
	if _, ok := idx.Resolve(mustParse(t, "http://example.com/aria2.pkg")); !ok {
		t.Fatal("aria2 target should be indexed after sidecar removal")
	}
}

func TestWatcherIndexesFilesInExistingSubdirectories(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "PPSA01234")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	idx, err := NewIndex(dir, LayoutBasename)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(idx, WatcherConfig{
		Settle:       200 * time.Millisecond,
		PollInterval: 50 * time.Millisecond,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	target := filepath.Join(nested, "nested.pkg")
	if err := os.WriteFile(target, []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, w.Events(), KindStable, "nested.pkg", 2*time.Second)
	if _, ok := idx.All()["nested.pkg"]; !ok {
		t.Fatal("nested.pkg missing from index")
	}
}

func TestWatcherIndexesFilesInNewSubdirectories(t *testing.T) {
	w, idx, dir := testWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	nested := filepath.Join(dir, "PPSA01234")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// Give fsnotify a moment to deliver the directory create event so the
	// watcher can add the new directory before the file lands inside it.
	time.Sleep(100 * time.Millisecond)
	target := filepath.Join(nested, "new-nested.pkg")
	if err := os.WriteFile(target, []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, w.Events(), KindStable, "new-nested.pkg", 2*time.Second)
	if _, ok := idx.All()["new-nested.pkg"]; !ok {
		t.Fatal("new-nested.pkg missing from index")
	}
}

func TestWatcherIndexesFilesInRenamedSubdirectory(t *testing.T) {
	w, idx, dir := testWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Build a populated directory outside the library, then move it in
	// atomically. fsnotify delivers a single create for the directory with the
	// file already inside it, so the watcher must scan the new subtree to find
	// the file — there is no per-file create event to react to.
	staging := dir + "-staging"
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "moved.pkg"), []byte("moved-in"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, filepath.Join(dir, "PPSA01234")); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, w.Events(), KindStable, "moved.pkg", 2*time.Second)
	if _, ok := idx.All()["moved.pkg"]; !ok {
		t.Fatal("moved.pkg missing from index")
	}
}

func TestWatcherEmitsRemovedOnDelete(t *testing.T) {
	w, idx, dir := testWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	target := filepath.Join(dir, "to-delete.pkg")
	if err := os.WriteFile(target, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, w.Events(), KindStable, "to-delete.pkg", 2*time.Second)

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, w.Events(), KindRemoved, "to-delete.pkg", 2*time.Second)

	if got, _ := idx.Stats(); got != 0 {
		t.Errorf("index should be empty after delete, got %d", got)
	}
}

func TestWatcherStableSizeZeroDoesNotFire(t *testing.T) {
	w, _, dir := testWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Touch an empty file. The watcher must not emit KindStable for it
	// (size > 0 is required by the policy).
	target := filepath.Join(dir, "empty.pkg")
	f, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case ev := <-w.Events():
			if ev.Kind == KindStable {
				t.Errorf("zero-byte file should not emit KindStable, got %+v", ev)
				return
			}
		case <-deadline:
			return // no KindStable observed within the window, as expected
		}
	}
}

func TestWatcherClosesEventsChannelOnContextCancel(t *testing.T) {
	w, _, _ := testWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	cancel()
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-w.Events():
			if !ok {
				return // events channel closed; clean shutdown
			}
		case <-deadline:
			t.Fatal("events channel was not closed after context cancel")
		}
	}
}
