package library

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNewIndexWalksRoot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.pkg"), 16)
	writeFile(t, filepath.Join(dir, "sub", "b.pkg"), 16)
	writeFile(t, filepath.Join(dir, "sub", "deep", "c.pkg"), 16)

	idx, err := NewIndex(dir, LayoutBasename)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	files, basenames := idx.Stats()
	if files != 3 || basenames != 3 {
		t.Errorf("Stats = (files=%d, basenames=%d), want (3, 3)", files, basenames)
	}
}

func TestNewIndexEmptyDirIsValid(t *testing.T) {
	dir := t.TempDir()
	idx, err := NewIndex(dir, LayoutBasename)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	files, _ := idx.Stats()
	if files != 0 {
		t.Errorf("empty index should have 0 files, got %d", files)
	}
}

func TestNewIndexNonexistentDirIsValid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist-yet")
	idx, err := NewIndex(dir, LayoutBasename)
	if err != nil {
		t.Fatalf("NewIndex on missing dir should not error: %v", err)
	}
	if idx.Root() == "" {
		t.Error("Root() should be populated even when dir is missing")
	}
}

func TestResolveByBasename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "PPSA01234_00-FAKEPKG_0.pkg")
	writeFile(t, target, 32)
	idx, err := NewIndex(dir, LayoutBasename)
	if err != nil {
		t.Fatal(err)
	}
	u := mustParse(t, "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01234_00-X/app/pkg/PPSA01234_00-FAKEPKG_0.pkg?downloadId=abc")
	got, ok := idx.Resolve(u)
	if !ok {
		t.Fatal("expected basename match")
	}
	if got != target {
		t.Errorf("Resolve = %q, want %q", got, target)
	}
}

func TestResolveMiss(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.pkg"), 32)
	idx, err := NewIndex(dir, LayoutBasename)
	if err != nil {
		t.Fatal(err)
	}
	u := mustParse(t, "http://example.com/path/nothing-here.pkg")
	if _, ok := idx.Resolve(u); ok {
		t.Error("expected miss")
	}
}

func TestResolveNilURL(t *testing.T) {
	dir := t.TempDir()
	idx, err := NewIndex(dir, LayoutBasename)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.Resolve(nil); ok {
		t.Error("nil URL should not resolve")
	}
}

func TestResolveAmbiguousBasenameBasenameLayout(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "titleA", "shared_0.pkg"), 32)
	writeFile(t, filepath.Join(dir, "titleB", "shared_0.pkg"), 32)
	idx, err := NewIndex(dir, LayoutBasename)
	if err != nil {
		t.Fatal(err)
	}
	u := mustParse(t, "http://example.com/path/shared_0.pkg")
	if _, ok := idx.Resolve(u); ok {
		t.Error("ambiguous basename in basename layout should not resolve")
	}
}

func TestResolveAmbiguousPerTitleDisambiguatesByHint(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "PPSA01234", "shared_0.pkg"), 32)
	writeFile(t, filepath.Join(dir, "PPSA09876", "shared_0.pkg"), 32)
	idx, err := NewIndex(dir, LayoutPerTitle)
	if err != nil {
		t.Fatal(err)
	}
	u := mustParse(t, "http://example.com/PPSA09876_00/app/pkg/shared_0.pkg")
	got, ok := idx.Resolve(u)
	if !ok {
		t.Fatal("per-title layout should disambiguate by hint")
	}
	if !filepath.IsAbs(got) || filepath.Base(filepath.Dir(got)) != "PPSA09876" {
		t.Errorf("Resolve = %q, expected PPSA09876 candidate", got)
	}
}

func TestResolveAmbiguousPerTitleNoHintStillAmbiguous(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "PPSA01234", "shared_0.pkg"), 32)
	writeFile(t, filepath.Join(dir, "PPSA09876", "shared_0.pkg"), 32)
	idx, err := NewIndex(dir, LayoutPerTitle)
	if err != nil {
		t.Fatal(err)
	}
	u := mustParse(t, "http://example.com/path/shared_0.pkg")
	if _, ok := idx.Resolve(u); ok {
		t.Error("per-title layout without title hint should remain ambiguous")
	}
}

func TestAddRemoveIdempotent(t *testing.T) {
	dir := t.TempDir()
	idx, err := NewIndex(dir, LayoutBasename)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "x.pkg")
	idx.Add(p)
	idx.Add(p)
	files, basenames := idx.Stats()
	if files != 1 || basenames != 1 {
		t.Errorf("Stats after double-Add = (%d, %d), want (1, 1)", files, basenames)
	}
	idx.Remove(p)
	idx.Remove(p) // idempotent
	files, basenames = idx.Stats()
	if files != 0 || basenames != 0 {
		t.Errorf("Stats after Remove = (%d, %d), want (0, 0)", files, basenames)
	}
}

func TestAllReturnsSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.pkg"), 16)
	idx, err := NewIndex(dir, LayoutBasename)
	if err != nil {
		t.Fatal(err)
	}
	all := idx.All()
	if len(all["a.pkg"]) != 1 {
		t.Errorf("All()[a.pkg] = %v, want one entry", all["a.pkg"])
	}
	// Mutating snapshot must not affect the index.
	all["a.pkg"] = nil
	if files, _ := idx.Stats(); files != 1 {
		t.Errorf("mutating snapshot affected the index")
	}
}

func TestExtractTitleHint(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/gst/prod/00/PPSA01234_00-X/app/pkg/x_0.pkg", "PPSA01234"},
		{"/random/CUSA12345_extra/file.pkg", "CUSA12345"},
		{"/gs2/appkgo/UP1234-CUSA12345_00/8/f_x/x_0.pkg", "UP1234-CUSA12345"},
		{"/no/title/here/file.pkg", ""},
	}
	for _, c := range cases {
		if got := extractTitleHint(c.in); got != c.want {
			t.Errorf("extractTitleHint(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
