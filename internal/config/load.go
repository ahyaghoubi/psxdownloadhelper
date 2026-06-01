package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Default returns a Config populated with the defaults documented in
// docs/configuration.md. Every resilience-layer feature ships off by
// default so the proxy behaves exactly as it did before ADR 0003 unless
// the user opts in.
func Default() *Config {
	return &Config{
		Proxy: ProxyConfig{Listen: "0.0.0.0:8080"},
		Admin: AdminConfig{Listen: "127.0.0.1:8081", AutoOpen: false},
		Library: LibraryConfig{
			Dir:            "~/Downloads/psxdh",
			Layout:         "basename",
			Watch:          true,
			StableSettleMs: 2000,
			IgnoreSuffixes: []string{".part", ".fdmdownload", ".tmp", ".crdownload"},
		},
		Match: MatchConfig{PS4: true, PS5: true},
		Capture: CaptureConfig{
			LogIgnored:         false,
			ExportFormats:      []string{"txt", "fdm", "aria2"},
			PrefetchSCMetadata: false,
			Persist:            PersistConfig{Enabled: false, FSync: false},
		},
		Handoff: HandoffConfig{
			FDM: FDMHandoffConfig{Enabled: true, FallbackToClipboard: true},
		},
		Forward: ForwardConfig{
			Mode:             "auto",
			PassthroughHTTPS: true,
			Retry: RetryConfig{
				MaxAttempts:      1,
				InitialBackoffMs: 200,
				MaxBackoffMs:     5000,
				Multiplier:       2.0,
				Jitter:           0.2,
			},
			PartialCache: PartialCacheConfig{
				Enabled:      false,
				MinSizeBytes: 1 << 20, // 1 MiB
			},
		},
		Network: NetworkConfig{
			DNS: DNSConfig{
				Mode:            "system",
				TimeoutMs:       1500,
				CacheTTLs:       300,
				CacheMaxEntries: 4096,
			},
			PreferIPv4:    false,
			DialTimeoutMs: 10000,
			UpstreamProxy: UpstreamProxyConfig{Enabled: false},
			Circuit: CircuitConfig{
				Enabled:          false,
				FailureThreshold: 5,
				CooldownMs:       30000,
			},
			Bandwidth: BandwidthConfig{ForwardBPS: 0, BurstBytes: 0},
		},
		Verify: VerifyConfig{CRC: false},
		Log:    LogConfig{Level: "info", JSON: false},
	}
}

// Load reads and validates a YAML config file. An empty path returns Default().
// YAML fields overlay defaults field-by-field; missing fields keep their default values.
func Load(path string) (*Config, error) {
	c := Default()
	if path == "" {
		return c, c.expandAndValidate()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	return c, c.expandAndValidate()
}

func (c *Config) expandAndValidate() error {
	if err := c.expandPaths(); err != nil {
		return err
	}
	return c.Validate()
}

func (c *Config) expandPaths() error {
	home := ""
	resolveTilde := func(p string) (string, error) {
		if !strings.HasPrefix(p, "~") {
			return p, nil
		}
		if home == "" {
			h, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve home directory: %w", err)
			}
			home = h
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	}

	expanded, err := resolveTilde(c.Library.Dir)
	if err != nil {
		return err
	}
	c.Library.Dir = expanded

	if c.Capture.Persist.Path != "" {
		expanded, err = resolveTilde(c.Capture.Persist.Path)
		if err != nil {
			return err
		}
		c.Capture.Persist.Path = expanded
	}
	return nil
}

func (c *Config) Validate() error {
	if err := validateListen(c.Proxy.Listen); err != nil {
		return fmt.Errorf("proxy.listen: %w", err)
	}
	if err := validateListen(c.Admin.Listen); err != nil {
		return fmt.Errorf("admin.listen: %w", err)
	}
	if c.Library.Dir == "" {
		return errors.New("library.dir is required")
	}
	switch c.Library.Layout {
	case "basename", "per-title":
	default:
		return fmt.Errorf("library.layout: must be 'basename' or 'per-title', got %q", c.Library.Layout)
	}
	if c.Library.StableSettleMs < 0 {
		return fmt.Errorf("library.stable_settle_ms: must be >= 0, got %d", c.Library.StableSettleMs)
	}
	switch c.Forward.Mode {
	case "auto", "cache", "strict":
	default:
		return fmt.Errorf("forward.mode: must be 'auto', 'cache' or 'strict', got %q", c.Forward.Mode)
	}
	if err := validateRetry(c.Forward.Retry); err != nil {
		return err
	}
	if err := validatePartialCache(c.Forward.PartialCache); err != nil {
		return err
	}
	if err := validateNetwork(c.Network); err != nil {
		return err
	}
	if err := validatePersist(c.Capture.Persist); err != nil {
		return err
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level: must be one of debug|info|warn|error, got %q", c.Log.Level)
	}
	return nil
}

func validateRetry(r RetryConfig) error {
	if r.MaxAttempts < 0 {
		return fmt.Errorf("forward.retry.max_attempts: must be >= 0, got %d", r.MaxAttempts)
	}
	if r.InitialBackoffMs < 0 {
		return fmt.Errorf("forward.retry.initial_backoff_ms: must be >= 0, got %d", r.InitialBackoffMs)
	}
	if r.MaxBackoffMs < 0 {
		return fmt.Errorf("forward.retry.max_backoff_ms: must be >= 0, got %d", r.MaxBackoffMs)
	}
	if r.MaxBackoffMs > 0 && r.InitialBackoffMs > r.MaxBackoffMs {
		return fmt.Errorf("forward.retry.initial_backoff_ms (%d) exceeds max_backoff_ms (%d)", r.InitialBackoffMs, r.MaxBackoffMs)
	}
	if r.Multiplier < 0 {
		return fmt.Errorf("forward.retry.multiplier: must be >= 0, got %v", r.Multiplier)
	}
	if r.Jitter < 0 || r.Jitter > 1 {
		return fmt.Errorf("forward.retry.jitter: must be in [0,1], got %v", r.Jitter)
	}
	return nil
}

func validatePartialCache(p PartialCacheConfig) error {
	if p.MinSizeBytes < 0 {
		return fmt.Errorf("forward.partial_cache.min_size_bytes: must be >= 0, got %d", p.MinSizeBytes)
	}
	return nil
}

func validateNetwork(n NetworkConfig) error {
	switch strings.ToLower(strings.TrimSpace(n.DNS.Mode)) {
	case "", "system", "udp", "doh", "doh+udp":
	default:
		return fmt.Errorf("network.dns.mode: must be system|udp|doh|doh+udp, got %q", n.DNS.Mode)
	}
	if n.DNS.TimeoutMs < 0 {
		return fmt.Errorf("network.dns.timeout_ms: must be >= 0, got %d", n.DNS.TimeoutMs)
	}
	if n.DNS.CacheTTLs < 0 {
		return fmt.Errorf("network.dns.cache_ttl_s: must be >= 0, got %d", n.DNS.CacheTTLs)
	}
	if n.DNS.CacheMaxEntries < 0 {
		return fmt.Errorf("network.dns.cache_max_entries: must be >= 0, got %d", n.DNS.CacheMaxEntries)
	}
	if n.DialTimeoutMs < 0 {
		return fmt.Errorf("network.dial_timeout_ms: must be >= 0, got %d", n.DialTimeoutMs)
	}
	if n.UpstreamProxy.Enabled && n.UpstreamProxy.URL == "" {
		return errors.New("network.upstream_proxy: url is required when enabled")
	}
	if n.UpstreamProxy.URL != "" {
		scheme := schemeOf(n.UpstreamProxy.URL)
		switch scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("network.upstream_proxy.url: scheme must be http/https/socks5, got %q in %q", scheme, n.UpstreamProxy.URL)
		}
	}
	if n.Circuit.FailureThreshold < 0 {
		return fmt.Errorf("network.circuit.failure_threshold: must be >= 0, got %d", n.Circuit.FailureThreshold)
	}
	if n.Circuit.CooldownMs < 0 {
		return fmt.Errorf("network.circuit.cooldown_ms: must be >= 0, got %d", n.Circuit.CooldownMs)
	}
	if n.Bandwidth.ForwardBPS < 0 {
		return fmt.Errorf("network.bandwidth.forward_bps: must be >= 0, got %d", n.Bandwidth.ForwardBPS)
	}
	if n.Bandwidth.BurstBytes < 0 {
		return fmt.Errorf("network.bandwidth.burst_bytes: must be >= 0, got %d", n.Bandwidth.BurstBytes)
	}
	return nil
}

func validatePersist(p PersistConfig) error {
	if p.Enabled && p.Path == "" {
		return errors.New("capture.persist: path is required when enabled")
	}
	return nil
}

func schemeOf(rawURL string) string {
	i := strings.Index(rawURL, "://")
	if i <= 0 {
		return ""
	}
	return strings.ToLower(rawURL[:i])
}

func validateListen(addr string) error {
	if addr == "" {
		return errors.New("required")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("invalid host:port %q: %w", addr, err)
	}
	return nil
}
