package downloader

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLocateAria2Explicit(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "aria2c")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := LocateAria2(bin)
	if err != nil {
		t.Fatal(err)
	}
	if got != bin {
		t.Errorf("got %q, want %q", got, bin)
	}
}

func TestLocateAria2MissingOnPath(t *testing.T) {
	_, err := LocateAria2(filepath.Join(t.TempDir(), "no-such-aria2c"))
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestInstallHintNonEmpty(t *testing.T) {
	hint := InstallHint()
	if hint == "" {
		t.Fatal("empty install hint")
	}
	switch runtime.GOOS {
	case "darwin":
		if hint != "brew install aria2" {
			t.Errorf("darwin hint = %q", hint)
		}
	case "linux":
		if hint != "sudo apt install aria2" {
			t.Errorf("linux hint = %q", hint)
		}
	case "windows":
		if hint != "winget install aria2.aria2" {
			t.Errorf("windows hint = %q", hint)
		}
	}
}
