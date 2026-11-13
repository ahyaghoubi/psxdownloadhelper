package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsAreValid(t *testing.T) {
	c, err := Load("", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Finalise(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
	if !filepath.IsAbs(c.Library.Dir) || strings.Contains(c.Library.Dir, "~") {
		t.Errorf("library dir not expanded: %q", c.Library.Dir)
	}
	if c.UpstreamProxyURL() != nil {
		t.Error("upstream proxy should be off by default")
	}
}

func TestMissingDefaultFileIsFine(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), false); err != nil {
		t.Fatalf("missing default config should fall back to defaults: %v", err)
	}
}

func TestMissingExplicitFileFails(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), true); err == nil {
		t.Fatal("missing --config file should be an error")
	}
}

// Config files written by older releases carry many removed sections. They
// must still load, and the keys we kept must still apply.
func TestLegacyConfigStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	legacy := `
proxy:
  listen: 0.0.0.0:9090
admin:
  enabled: true
  listen: 0.0.0.0:8081
library:
  dir: /tmp/psx-lib
  layout: basename
  stable_settle_ms: 2000
cluster:
  enabled: true
  role: master
network:
  dns:
    mode: doh+udp
  upstream_proxy:
    enabled: true
    url: socks5://127.0.0.1:1080
log:
  level: debug
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Finalise(); err != nil {
		t.Fatal(err)
	}
	if c.Proxy.Listen != "0.0.0.0:9090" || c.Library.Dir != filepath.Clean("/tmp/psx-lib") || c.Log.Level != "debug" {
		t.Errorf("kept keys not applied: %+v", c)
	}
	if u := c.UpstreamProxyURL(); u == nil || u.Scheme != "socks5" {
		t.Errorf("upstream proxy = %v", u)
	}
	if len(c.Library.IgnoreSuffixes) == 0 {
		t.Error("ignore suffixes should keep their defaults")
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]func(*Config){
		"bad listen":        func(c *Config) { c.Proxy.Listen = "8080" },
		"bad proxy scheme":  func(c *Config) { c.Network.UpstreamProxy.Enabled = true; c.Network.UpstreamProxy.URL = "ftp://x:1" },
		"proxy without url": func(c *Config) { c.Network.UpstreamProxy.Enabled = true },
		"bad log level":     func(c *Config) { c.Log.Level = "loud" },
	}
	for name, mutate := range cases {
		c := Default()
		mutate(c)
		if err := c.Finalise(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
