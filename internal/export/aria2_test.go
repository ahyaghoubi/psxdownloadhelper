package export

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteAria2(t *testing.T) {
	var buf bytes.Buffer
	urls := []string{
		"http://cdn.example/a/GAME_0.pkg?sig=abc",
		"  ", // skipped
		"http://cdn.example/b/GAME_1.pkg",
	}
	if err := WriteAria2(&buf, urls, "/home/me/lib"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// 2 urls × 3 lines (url, out=, dir=) = 6 lines.
	if len(lines) != 6 {
		t.Fatalf("expected 6 lines, got %d:\n%s", len(lines), out)
	}
	if lines[0] != "http://cdn.example/a/GAME_0.pkg?sig=abc" {
		t.Errorf("line 0 = %q", lines[0])
	}
	if lines[1] != "  out=GAME_0.pkg" {
		t.Errorf("line 1 = %q, want indented out=GAME_0.pkg", lines[1])
	}
	if lines[2] != "  dir=/home/me/lib" {
		t.Errorf("line 2 = %q", lines[2])
	}
}

func TestWriteAria2NoDir(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteAria2(&buf, []string{"http://x/y.pkg"}, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "dir=") {
		t.Errorf("no dir= line expected when dir is empty:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "out=y.pkg") {
		t.Errorf("expected out=y.pkg:\n%s", buf.String())
	}
}
