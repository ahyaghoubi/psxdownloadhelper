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
