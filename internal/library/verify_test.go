package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/verify"
)

// drainStable runs a watcher until the named basename reports KindStable or
// the deadline elapses.
func drainStable(t *testing.T, w *Watcher, basename string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()
	for {
		select {
		case ev := <-w.Events():
			if ev.Kind == KindStable && ev.Basename == basename {
				return
			}
		case <-ctx.Done():
			t.Fatalf("never saw KindStable for %s", basename)
		}
	}
}

func newVerifyWatcher(t *testing.T, dir string) (*Index, *Watcher) {
	t.Helper()
	idx, err := NewIndex(dir, LayoutBasename)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(idx, WatcherConfig{
		Settle:       50 * time.Millisecond,
		PollInterval: 20 * time.Millisecond,
		Verifier:     verify.DefaultVerifier(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return idx, w
}

func TestWatcherMarksVerifyOK(t *testing.T) {
	dir := t.TempDir()
	idx, w := newVerifyWatcher(t, dir)

	body := []byte("good package bytes")
	sum := sha256.Sum256(body)
	pkg := filepath.Join(dir, "good.pkg")
	if err := os.WriteFile(pkg, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pkg+".crc", []byte("sha256 "+hex.EncodeToString(sum[:])), 0o644); err != nil {
		t.Fatal(err)
	}
	drainStable(t, w, "good.pkg")

	if got := idx.VerifyStateOf(pkg); got != VerifyOK {
		t.Errorf("VerifyStateOf(good.pkg) = %v, want VerifyOK", got)
	}
}

func TestWatcherMarksVerifyFailed(t *testing.T) {
	dir := t.TempDir()
	idx, w := newVerifyWatcher(t, dir)

	pkg := filepath.Join(dir, "bad.pkg")
	if err := os.WriteFile(pkg, []byte("corrupted bytes on a bad link"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A SHA-256 of some *other* content — guaranteed mismatch.
	wrong := sha256.Sum256([]byte("the original, correct content"))
	if err := os.WriteFile(pkg+".crc", []byte("sha256 "+hex.EncodeToString(wrong[:])), 0o644); err != nil {
		t.Fatal(err)
	}
	drainStable(t, w, "bad.pkg")

	if got := idx.VerifyStateOf(pkg); got != VerifyFailed {
		t.Errorf("VerifyStateOf(bad.pkg) = %v, want VerifyFailed", got)
	}
}

func TestWatcherNoSidecarStaysUnchecked(t *testing.T) {
	dir := t.TempDir()
	idx, w := newVerifyWatcher(t, dir)

	pkg := filepath.Join(dir, "plain.pkg")
	if err := os.WriteFile(pkg, []byte("no sidecar here"), 0o644); err != nil {
		t.Fatal(err)
	}
	drainStable(t, w, "plain.pkg")

	if got := idx.VerifyStateOf(pkg); got != VerifyUnchecked {
		t.Errorf("VerifyStateOf(plain.pkg) = %v, want VerifyUnchecked", got)
	}
}
