package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var suffixes = []string{".part", ".fdmdownload", ".aria2"}

// clock is a manual clock so settle behaviour is tested without sleeping.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock                   { return &clock{t: time.Now()} }
func write(t *testing.T, p string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newLib(t *testing.T, root string, c *clock) *Library {
	t.Helper()
	l, err := New(root, suffixes, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	l.now = c.now
	l.Scan()
	return l
}

func TestCreatesMissingFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a", "b")
	if _, err := New(root, suffixes, time.Second); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Fatalf("library folder not created: %v", err)
	}
}

func TestOldFileIsReadyImmediately(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "game_0.pkg")
	write(t, p, 10)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(p, old, old)

	l := newLib(t, root, newClock())
	f, ok := l.Lookup("game_0.pkg")
	if !ok || f.Path != p || f.Size != 10 {
		t.Fatalf("Lookup = %+v, %v", f, ok)
	}
}

func TestNewFileWaitsForSettle(t *testing.T) {
	root := t.TempDir()
	c := newClock()
	l := newLib(t, root, c)

	p := filepath.Join(root, "game_0.pkg")
	write(t, p, 10)
	now := c.t
	_ = os.Chtimes(p, now, now)
	l.Scan()
	if _, ok := l.Lookup("game_0.pkg"); ok {
		t.Fatal("fresh file must not be ready before the settle window")
	}

	// Still growing: the settle window restarts.
	c.advance(time.Second)
	write(t, p, 20)
	_ = os.Chtimes(p, c.t, c.t)
	l.Scan()
	c.advance(time.Second)
	l.Scan()
	if _, ok := l.Lookup("game_0.pkg"); ok {
		t.Fatal("file that just grew must not be ready")
	}

	c.advance(2 * time.Second)
	l.Scan()
	if f, ok := l.Lookup("game_0.pkg"); !ok || f.Size != 20 {
		t.Fatalf("settled file should be ready: %+v %v", f, ok)
	}
}

func TestTempNamesAreWritingNotReady(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "game_1.pkg.fdmdownload"), 5)
	write(t, filepath.Join(root, "game_2.pkg.PART"), 5)
	old := time.Now().Add(-time.Hour)
	for _, n := range []string{"game_1.pkg.fdmdownload", "game_2.pkg.PART"} {
		_ = os.Chtimes(filepath.Join(root, n), old, old)
	}
	l := newLib(t, root, newClock())

	if _, ok := l.Lookup("game_1.pkg"); ok {
		t.Fatal("temp file must never be served")
	}
	f, ok := l.Find("game_1.pkg")
	if !ok || f.State != StateWriting || f.Size != 5 {
		t.Fatalf("Find should report the in-progress download: %+v %v", f, ok)
	}
	if f, ok := l.Find("game_2.pkg"); !ok || f.State != StateWriting {
		t.Fatalf("suffix match should ignore case: %+v %v", f, ok)
	}
}

func TestAria2ControlFileBlocksReady(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "game_0.pkg")
	write(t, p, 10)
	write(t, p+".aria2", 1)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(p, old, old)
	l := newLib(t, root, newClock())
	if _, ok := l.Lookup("game_0.pkg"); ok {
		t.Fatal("file with .aria2 control file is still downloading")
	}
	_ = os.Remove(p + ".aria2")
	l.Scan()
	if _, ok := l.Lookup("game_0.pkg"); !ok {
		t.Fatal("file should be ready once the control file is gone")
	}
}

func TestSubfoldersAndHiddenFolders(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "PPSA01234", "game_0.pkg"), 10)
	write(t, filepath.Join(root, ".psxdh-partial", "game_1.pkg"), 10)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(root, "PPSA01234", "game_0.pkg"), old, old)
	_ = os.Chtimes(filepath.Join(root, ".psxdh-partial", "game_1.pkg"), old, old)
	l := newLib(t, root, newClock())

	if f, ok := l.Lookup("game_0.pkg"); !ok || f.Rel != "PPSA01234/game_0.pkg" {
		t.Fatalf("subfolder file: %+v %v", f, ok)
	}
	if _, ok := l.Lookup("game_1.pkg"); ok {
		t.Fatal("files in hidden folders must be ignored")
	}
}

func TestDuplicatesPickLargest(t *testing.T) {
	root := t.TempDir()
	small := filepath.Join(root, "a", "game_0.pkg")
	big := filepath.Join(root, "b", "game_0.pkg")
	write(t, small, 5)
	write(t, big, 50)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(small, old, old)
	_ = os.Chtimes(big, old, old)
	l := newLib(t, root, newClock())
	if f, ok := l.Lookup("game_0.pkg"); !ok || f.Path != big {
		t.Fatalf("Lookup = %+v %v; want %s", f, ok, big)
	}
}

func TestRemovedFileDisappears(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "game_0.pkg")
	write(t, p, 10)
	l := newLib(t, root, newClock())
	_ = os.Remove(p)
	l.Scan()
	if len(l.Files()) != 0 {
		t.Fatalf("removed file still listed: %+v", l.Files())
	}
}

func TestEmptyFileNeverReady(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "game_0.pkg")
	write(t, p, 0)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(p, old, old)
	l := newLib(t, root, newClock())
	if _, ok := l.Lookup("game_0.pkg"); ok {
		t.Fatal("zero-byte file must not be served")
	}
}
