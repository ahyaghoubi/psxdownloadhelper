package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Default returns a Config populated with the defaults documented in
// docs/configuration.md. Defaults target PS5 on Iranian networks: dashboard
// on, cluster master with master-as-node, DoH+UDP resolvers, forward retry,
// partial cache, and integrity verification. Users elsewhere can override via
// YAML.
func Default() *Config {
	return &Config{
		Proxy: ProxyConfig{Listen: "0.0.0.0:8080"},
		// Dashboard binds the LAN by default so a phone can reach it; a token is
		// required for any non-loopback bind and auto-generated at startup when empty.
		Admin: AdminConfig{Enabled: true, Listen: "0.0.0.0:8081", AutoOpen: false},
		Library: LibraryConfig{
			Dir:            "~/Downloads/psxdh",
			Layout:         "basename",
			Watch:          true,
			StableSettleMs: 2000,
			IgnoreSuffixes: []string{".part", ".fdmdownload", ".tmp", ".crdownload"},
		},
		Match: MatchConfig{PS4: false, PS5: true},
		Capture: CaptureConfig{
			LogIgnored:         false,
			ExportFormats:      []string{"txt", "fdm", "aria2"},
			PrefetchSCMetadata: false,
			Persist:            PersistConfig{Enabled: false, FSync: false},
		},
		Handoff: HandoffConfig{
			FDM: FDMHandoffConfig{Enabled: true, FallbackToClipboard: true},
			Aria2: Aria2HandoffConfig{
				Enabled:  false,
				RPCURL:   "http://127.0.0.1:6800/jsonrpc",
				AutoPush: false,
			},
		},
		Forward: ForwardConfig{
			Mode:             "auto",
			PassthroughHTTPS: true,
			Retry: RetryConfig{
				MaxAttempts:      4,
				InitialBackoffMs: 250,
				MaxBackoffMs:     4000,
				Multiplier:       2.0,
				Jitter:           0.2,
			},
			PartialCache: PartialCacheConfig{
				Enabled:      true,
				MinSizeBytes: 1 << 20, // 1 MiB
				Resume:       true,
			},
		},
		Network: NetworkConfig{
			DNS: DNSConfig{
				Mode: "doh+udp",
				Resolvers: []string{
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
				},
				TimeoutMs:       1500,
				CacheTTLs:       300,
				CacheMaxEntries: 4096,
				Health:          DNSHealthConfig{Enabled: true, ReprobeIntervalMs: 60000},
			},
			PreferIPv4:    true,
			DialTimeoutMs: 10000,
			UpstreamProxy: UpstreamProxyConfig{Enabled: false},
			Circuit: CircuitConfig{
				Enabled:          false,
				FailureThreshold: 5,
				CooldownMs:       30000,
			},
			Bandwidth: BandwidthConfig{ForwardBPS: 0, BurstBytes: 0},
		},
		Verify: VerifyConfig{CRC: false, OnStable: true, RequireSizeMatch: false},
		MDNS:   MDNSConfig{Enabled: false, InstanceName: "psxdh"},
		Downloader: DownloaderConfig{
			Engine:               "aria2",
			RPCPort:              6800,
			ConnectionsPerServer: 8,
			Split:                8,
			MaxConcurrent:        4,
		},
		Cluster: ClusterConfig{
			Enabled:      true,
			Role:         "master",
			Bind:         "0.0.0.0:8082",
			MasterAsNode: true,
		},
		Jobs: JobsConfig{ImportEnumerate: true},
		Log:  LogConfig{Level: "info", JSON: false},
	}
}

// DefaultConfigPath returns the path used for dashboard edits when --config is
// not passed. Settings save here on first use; if the file already exists it is
// loaded at startup.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "psxdh", "config.yaml"), nil
}

// ResolveConfigPath maps the --config flag to load and persist paths. When flag
// is empty, persistPath is always DefaultConfigPath(); loadPath is that file when
// it exists, otherwise "" (built-in defaults). This keeps zero-config startup
// while letting the dashboard write settings without requiring --config.
func ResolveConfigPath(flag string) (loadPath, persistPath string, err error) {
	if flag != "" {
		return flag, flag, nil
	}
	persistPath, err = DefaultConfigPath()
	if err != nil {
		return "", "", err
	}
	if _, statErr := os.Stat(persistPath); statErr == nil {
		loadPath = persistPath
	}
	return loadPath, persistPath, nil
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

// Marshal serialises the config back to YAML. Used by the dashboard's config
// editor to persist edits to config.yaml. The round-trip Load→Marshal→Load is
// stable for every field.
func (c *Config) Marshal() ([]byte, error) {
	return yaml.Marshal(c)
}

// ParseAndValidate overlays YAML data onto the defaults and validates the
// result, without reading or writing any file. The dashboard config editor uses
// it to check an edit before persisting it.
func ParseAndValidate(data []byte) (*Config, error) {
	c := Default()
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return c, c.expandAndValidate()
}

// ParseJSONAndValidate is the JSON twin of ParseAndValidate. The dashboard's
// structured config editor posts JSON; this overlays it onto the defaults and
// validates the result so a partial form submission still produces a complete
// config.
func ParseJSONAndValidate(data []byte) (*Config, error) {
	c := Default()
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
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
	if c.Jobs.ImportOnStart != "" {
		expanded, err = resolveTilde(c.Jobs.ImportOnStart)
		if err != nil {
			return err
		}
		c.Jobs.ImportOnStart = expanded
	}
	if c.Jobs.StatePath != "" {
		expanded, err = resolveTilde(c.Jobs.StatePath)
		if err != nil {
			return err
		}
		c.Jobs.StatePath = expanded
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
	if c.Handoff.Aria2.Enabled && c.Handoff.Aria2.RPCURL == "" {
		return errors.New("handoff.aria2: rpc_url is required when enabled")
	}
	if err := validateCluster(c.Cluster); err != nil {
		return err
	}
	if err := validateDownloader(c.Downloader); err != nil {
		return err
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level: must be one of debug|info|warn|error, got %q", c.Log.Level)
	}
	return nil
}

// NeedsEmbeddedDownloader reports whether this config starts the managed
// aria2c/HTTP downloader (psxdh node, or proxy with cluster.master_as_node).
func (c *Config) NeedsEmbeddedDownloader() bool {
	if !c.Cluster.Enabled {
		return false
	}
	if c.Cluster.Role == "slave" {
		return true
	}
	return c.Cluster.MasterAsNode
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
	if n.DNS.Health.ReprobeIntervalMs < 0 {
		return fmt.Errorf("network.dns.health.reprobe_interval_ms: must be >= 0, got %d", n.DNS.Health.ReprobeIntervalMs)
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

func validateCluster(c ClusterConfig) error {
	if !c.Enabled {
		return nil
	}
	switch c.Role {
	case "master", "slave":
	default:
		return fmt.Errorf("cluster.role: must be 'master' or 'slave', got %q", c.Role)
	}
	if err := validateListen(c.Bind); err != nil {
		return fmt.Errorf("cluster.bind: %w", err)
	}
	if c.Role == "slave" && c.MasterURL == "" {
		return errors.New("cluster.master_url is required for a slave node")
	}
	return nil
}

func validateDownloader(d DownloaderConfig) error {
	switch d.Engine {
	case "", "aria2":
	default:
		return fmt.Errorf("downloader.engine: only 'aria2' is supported, got %q", d.Engine)
	}
	if d.RPCPort < 0 || d.RPCPort > 65535 {
		return fmt.Errorf("downloader.rpc_port: must be 0-65535, got %d", d.RPCPort)
	}
	if d.ConnectionsPerServer < 0 {
		return fmt.Errorf("downloader.connections_per_server: must be >= 0, got %d", d.ConnectionsPerServer)
	}
	if d.Split < 0 {
		return fmt.Errorf("downloader.split: must be >= 0, got %d", d.Split)
	}
	if d.MaxConcurrent < 0 {
		return fmt.Errorf("downloader.max_concurrent: must be >= 0, got %d", d.MaxConcurrent)
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
