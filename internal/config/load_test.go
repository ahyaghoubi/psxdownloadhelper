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

func TestDefaultIranProfile(t *testing.T) {
	c := Default()

	if !c.Admin.Enabled {
		t.Error("admin.enabled should be true")
	}
	if c.Match.PS4 || !c.Match.PS5 {
		t.Errorf("match should be PS5-only, got ps4=%v ps5=%v", c.Match.PS4, c.Match.PS5)
	}
	if !c.Cluster.Enabled || c.Cluster.Role != "master" || !c.Cluster.MasterAsNode {
		t.Errorf("cluster = enabled:%v role:%q master_as_node:%v", c.Cluster.Enabled, c.Cluster.Role, c.Cluster.MasterAsNode)
	}
	if c.Network.DNS.Mode != "doh+udp" {
		t.Errorf("dns.mode = %q, want doh+udp", c.Network.DNS.Mode)
	}
	wantResolvers := []string{
		"1.1.1.1",
		"9.9.9.9",
		"8.8.8.8",
		"8.8.4.4",
		"178.22.122.100",
		"185.51.200.2",
		"https://dns.electrotm.org/dns-query",
		"https://free.shecan.ir/dns-query",
		"https://1.1.1.1/dns-query",
		"https://dns.google/dns-query",
	}
	if len(c.Network.DNS.Resolvers) != len(wantResolvers) {
		t.Fatalf("dns.resolvers len = %d, want %d", len(c.Network.DNS.Resolvers), len(wantResolvers))
	}
	for i, want := range wantResolvers {
		if c.Network.DNS.Resolvers[i] != want {
			t.Errorf("dns.resolvers[%d] = %q, want %q", i, c.Network.DNS.Resolvers[i], want)
		}
	}
	if !c.Network.DNS.Health.Enabled {
		t.Error("dns.health.enabled should be true")
	}
	if !c.Network.PreferIPv4 {
		t.Error("network.prefer_ipv4 should be true")
	}
	if c.Forward.Retry.MaxAttempts != 4 {
		t.Errorf("forward.retry.max_attempts = %d, want 4", c.Forward.Retry.MaxAttempts)
	}
	if !c.Forward.PartialCache.Enabled {
		t.Error("forward.partial_cache.enabled should be true")
	}
	if !c.Verify.OnStable {
		t.Error("verify.on_stable should be true")
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

func TestValidateRetryNegativeAttempts(t *testing.T) {
	c := Default()
	c.Forward.Retry.MaxAttempts = -1
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "forward.retry.max_attempts") {
		t.Errorf("expected retry validation error, got %v", err)
	}
}

func TestValidateRetryInitialExceedsMax(t *testing.T) {
	c := Default()
	c.Forward.Retry.InitialBackoffMs = 10000
	c.Forward.Retry.MaxBackoffMs = 1000
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "initial_backoff_ms") {
		t.Errorf("expected initial>max error, got %v", err)
	}
}

func TestValidateRetryJitterRange(t *testing.T) {
	c := Default()
	c.Forward.Retry.Jitter = 1.5
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "jitter") {
		t.Errorf("expected jitter error, got %v", err)
	}
}

func TestValidateDNSModes(t *testing.T) {
	for _, m := range []string{"", "system", "udp", "doh", "doh+udp"} {
		c := Default()
		c.Network.DNS.Mode = m
		if err := c.Validate(); err != nil {
			t.Errorf("DNS mode %q should validate: %v", m, err)
		}
	}
	c := Default()
	c.Network.DNS.Mode = "ouija"
	if err := c.Validate(); err == nil {
		t.Error("expected error for unknown DNS mode")
	}
}

func TestValidateUpstreamProxyRequiresURL(t *testing.T) {
	c := Default()
	c.Network.UpstreamProxy.Enabled = true
	c.Network.UpstreamProxy.URL = ""
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "upstream_proxy") {
		t.Errorf("expected upstream_proxy url error, got %v", err)
	}
}

func TestValidateUpstreamProxyScheme(t *testing.T) {
	c := Default()
	c.Network.UpstreamProxy.URL = "ftp://nope"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Errorf("expected scheme error, got %v", err)
	}
	for _, u := range []string{
		"http://localhost:8888",
		"https://localhost:8888",
		"socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080",
	} {
		c := Default()
		c.Network.UpstreamProxy.URL = u
		if err := c.Validate(); err != nil {
			t.Errorf("scheme of %q should validate: %v", u, err)
		}
	}
}

func TestValidatePersistRequiresPathWhenEnabled(t *testing.T) {
	c := Default()
	c.Capture.Persist.Enabled = true
	c.Capture.Persist.Path = ""
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "capture.persist") {
		t.Errorf("expected persist path error, got %v", err)
	}
}

func TestValidateBandwidthNonNegative(t *testing.T) {
	c := Default()
	c.Network.Bandwidth.ForwardBPS = -1
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "forward_bps") {
		t.Errorf("expected forward_bps error, got %v", err)
	}
}

func TestPersistPathExpansion(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	body := "capture:\n  persist:\n    enabled: true\n    path: \"~/psxdh-capture.jsonl\"\n"
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if strings.HasPrefix(c.Capture.Persist.Path, "~") {
		t.Errorf("persist path not expanded: %q", c.Capture.Persist.Path)
	}
}

func TestLoadDownloaderAllowHTTPFallback(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	body := "downloader:\n  allow_http_fallback: true\n"
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !c.Downloader.AllowHTTPFallback {
		t.Error("allow_http_fallback should be true")
	}
}

func TestNeedsEmbeddedDownloader(t *testing.T) {
	c := Default()
	if !c.NeedsEmbeddedDownloader() {
		t.Error("default config should need embedded downloader (cluster + master_as_node)")
	}
	c.Cluster.Enabled = false
	if c.NeedsEmbeddedDownloader() {
		t.Error("cluster disabled should not need embedded downloader")
	}
	c = Default()
	c.Cluster.MasterAsNode = false
	if c.NeedsEmbeddedDownloader() {
		t.Error("master without master_as_node should not need embedded downloader")
	}
	c.Cluster.Role = "slave"
	if !c.NeedsEmbeddedDownloader() {
		t.Error("slave always needs embedded downloader")
	}
}

func TestLoadCanonicalExampleIncludesResilienceFields(t *testing.T) {
	c, err := Load("testdata/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Forward.Retry.MaxAttempts != 1 {
		t.Errorf("default retry max_attempts = %d, want 1", c.Forward.Retry.MaxAttempts)
	}
	if c.Network.DNS.Mode != "system" {
		t.Errorf("default dns mode = %q, want system", c.Network.DNS.Mode)
	}
	if c.Network.DNS.CacheMaxEntries != 4096 {
		t.Errorf("default cache_max_entries = %d, want 4096", c.Network.DNS.CacheMaxEntries)
	}
}

func TestResolveConfigPath(t *testing.T) {
	t.Run("explicit flag", func(t *testing.T) {
		load, persist, err := ResolveConfigPath("/etc/psxdh/config.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if load != "/etc/psxdh/config.yaml" || persist != "/etc/psxdh/config.yaml" {
			t.Fatalf("got load=%q persist=%q", load, persist)
		}
	})

	t.Run("default without file", func(t *testing.T) {
		load, persist, err := ResolveConfigPath("")
		if err != nil {
			t.Fatal(err)
		}
		if load != "" {
			t.Errorf("load = %q, want empty when default file missing", load)
		}
		if !strings.HasSuffix(persist, filepath.Join(".config", "psxdh", "config.yaml")) {
			t.Errorf("persist = %q, want ~/.config/psxdh/config.yaml", persist)
		}
	})

	t.Run("default with existing file", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", dir)
		persist, err := DefaultConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(persist), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(persist, []byte("log:\n  level: warn\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		load, gotPersist, err := ResolveConfigPath("")
		if err != nil {
			t.Fatal(err)
		}
		if load != persist || gotPersist != persist {
			t.Fatalf("load=%q persist=%q", load, gotPersist)
		}
	})
}
