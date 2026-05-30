package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultValidates(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default config should validate, got: %v", err)
	}
}

func TestLoadCanonicalExample(t *testing.T) {
	c, err := Load("testdata/config.yaml")
	if err != nil {
		t.Fatalf("load canonical config: %v", err)
	}
	if c.Proxy.Listen != "0.0.0.0:8080" {
		t.Errorf("proxy.listen = %q, want 0.0.0.0:8080", c.Proxy.Listen)
	}
	if c.Admin.Listen != "127.0.0.1:8081" {
		t.Errorf("admin.listen = %q, want 127.0.0.1:8081", c.Admin.Listen)
	}
	if c.Library.Layout != "basename" {
		t.Errorf("library.layout = %q, want basename", c.Library.Layout)
	}
	if c.Library.StableSettleMs != 2000 {
		t.Errorf("library.stable_settle_ms = %d, want 2000", c.Library.StableSettleMs)
	}
	if len(c.Library.IgnoreSuffixes) != 4 {
		t.Errorf("library.ignore_suffixes len = %d, want 4", len(c.Library.IgnoreSuffixes))
	}
	if c.Forward.Mode != "auto" {
		t.Errorf("forward.mode = %q, want auto", c.Forward.Mode)
	}
}

func TestHomeExpansion(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	body := "library:\n  dir: \"~/foo\"\n"
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "foo")
	if c.Library.Dir != want {
		t.Errorf("library.dir = %q, want %q", c.Library.Dir, want)
	}
}

func TestPartialOverridePreservesDefaults(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	body := "library:\n  watch: false\n"
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Library.Watch {
		t.Error("library.watch should be overridden to false")
	}
	if c.Library.Layout != "basename" {
		t.Errorf("library.layout should retain default basename, got %q", c.Library.Layout)
	}
	if c.Library.StableSettleMs != 2000 {
		t.Errorf("library.stable_settle_ms should retain default 2000, got %d", c.Library.StableSettleMs)
	}
	if len(c.Library.IgnoreSuffixes) != 4 {
		t.Errorf("library.ignore_suffixes should retain default list, got len=%d", len(c.Library.IgnoreSuffixes))
	}
	if c.Forward.Mode != "auto" {
		t.Errorf("forward.mode should retain default auto, got %q", c.Forward.Mode)
	}
}

func TestEmptyPathReturnsDefaults(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") should return defaults, got: %v", err)
	}
	if c.Proxy.Listen != "0.0.0.0:8080" {
		t.Errorf("default proxy.listen = %q", c.Proxy.Listen)
	}
}

func TestStableSettleHelper(t *testing.T) {
	c := Default()
	if got := c.Library.StableSettle(); got.Milliseconds() != 2000 {
		t.Errorf("StableSettle = %v, want 2s", got)
	}
}

func TestValidateRejectsBadListen(t *testing.T) {
	c := Default()
	c.Proxy.Listen = "not-a-listen"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "proxy.listen") {
		t.Errorf("expected proxy.listen validation error, got %v", err)
	}
}

func TestValidateRejectsEmptyListen(t *testing.T) {
	c := Default()
	c.Admin.Listen = ""
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "admin.listen") {
		t.Errorf("expected admin.listen validation error, got %v", err)
	}
}

func TestValidateRejectsBadLayout(t *testing.T) {
	c := Default()
	c.Library.Layout = "unknown"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "library.layout") {
		t.Errorf("expected library.layout validation error, got %v", err)
	}
}

func TestValidateRejectsBadForwardMode(t *testing.T) {
	c := Default()
	c.Forward.Mode = "permissive"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "forward.mode") {
		t.Errorf("expected forward.mode validation error, got %v", err)
	}
}

func TestValidateAcceptsAllForwardModes(t *testing.T) {
	for _, mode := range []string{"auto", "cache", "strict"} {
		c := Default()
		c.Forward.Mode = mode
		if err := c.Validate(); err != nil {
			t.Errorf("forward.mode %q should validate, got %v", mode, err)
		}
	}
}

func TestValidateRejectsBadLogLevel(t *testing.T) {
	c := Default()
	c.Log.Level = "verbose"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "log.level") {
		t.Errorf("expected log.level validation error, got %v", err)
	}
}

func TestValidateRejectsNegativeSettleMs(t *testing.T) {
	c := Default()
	c.Library.StableSettleMs = -1
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "stable_settle_ms") {
		t.Errorf("expected stable_settle_ms validation error, got %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/that/does/not/exist.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(yamlPath, []byte("proxy: [this is not a map\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(yamlPath)
	if err == nil {
		t.Error("expected parse error for malformed YAML")
	}
}
