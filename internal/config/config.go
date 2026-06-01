// Package config loads and validates psxdh's runtime configuration.
// The on-disk schema is YAML and is documented in docs/configuration.md.
package config

import "time"

type Config struct {
	Proxy   ProxyConfig   `yaml:"proxy"`
	Admin   AdminConfig   `yaml:"admin"`
	Library LibraryConfig `yaml:"library"`
	Match   MatchConfig   `yaml:"match"`
	Capture CaptureConfig `yaml:"capture"`
	Handoff HandoffConfig `yaml:"handoff"`
	Forward ForwardConfig `yaml:"forward"`
	Network NetworkConfig `yaml:"network"`
	Verify  VerifyConfig  `yaml:"verify"`
	Log     LogConfig     `yaml:"log"`
}

type ProxyConfig struct {
	Listen string `yaml:"listen"`
}

type AdminConfig struct {
	Listen   string `yaml:"listen"`
	AutoOpen bool   `yaml:"auto_open"`
}

type LibraryConfig struct {
	Dir            string   `yaml:"dir"`
	Layout         string   `yaml:"layout"`
	Watch          bool     `yaml:"watch"`
	StableSettleMs int      `yaml:"stable_settle_ms"`
	IgnoreSuffixes []string `yaml:"ignore_suffixes"`
}

func (l LibraryConfig) StableSettle() time.Duration {
	return time.Duration(l.StableSettleMs) * time.Millisecond
}

type MatchConfig struct {
	PS4      bool   `yaml:"ps4"`
	PS5      bool   `yaml:"ps5"`
	RulesDir string `yaml:"rules_dir"`
}

type CaptureConfig struct {
	LogIgnored         bool          `yaml:"log_ignored"`
	ExportFormats      []string      `yaml:"export_formats"`
	PrefetchSCMetadata bool          `yaml:"prefetch_sc_metadata"`
	Persist            PersistConfig `yaml:"persist"`
}

// PersistConfig controls the append-only JSONL log of capture events.
// When Enabled, the proxy writes one JSON object per line to Path so a
// crash or restart never loses an observed URL.
type PersistConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
	FSync   bool   `yaml:"fsync"`
}

type FDMHandoffConfig struct {
	Enabled             bool   `yaml:"enabled"`
	FDMBinary           string `yaml:"fdm_binary"`
	FallbackToClipboard bool   `yaml:"fallback_to_clipboard"`
}

type HandoffConfig struct {
	FDM FDMHandoffConfig `yaml:"fdm"`
}

type ForwardConfig struct {
	Mode             string             `yaml:"mode"`
	PassthroughHTTPS bool               `yaml:"passthrough_https"`
	Retry            RetryConfig        `yaml:"retry"`
	PartialCache     PartialCacheConfig `yaml:"partial_cache"`
}

// RetryConfig is the backoff policy applied to upstream forward attempts.
// MaxAttempts of 1 disables retry; values <= 0 are normalised to 1.
type RetryConfig struct {
	MaxAttempts      int     `yaml:"max_attempts"`
	InitialBackoffMs int     `yaml:"initial_backoff_ms"`
	MaxBackoffMs     int     `yaml:"max_backoff_ms"`
	Multiplier       float64 `yaml:"multiplier"`
	Jitter           float64 `yaml:"jitter"`
}

// InitialBackoff returns the policy field as a time.Duration.
func (r RetryConfig) InitialBackoff() time.Duration {
	return time.Duration(r.InitialBackoffMs) * time.Millisecond
}

// MaxBackoff returns the policy field as a time.Duration.
func (r RetryConfig) MaxBackoff() time.Duration {
	return time.Duration(r.MaxBackoffMs) * time.Millisecond
}

// PartialCacheConfig controls whether the forward path tees its response
// to disk in library.dir and atomically renames on success.
type PartialCacheConfig struct {
	Enabled      bool  `yaml:"enabled"`
	MinSizeBytes int64 `yaml:"min_size_bytes"`
}

// NetworkConfig groups every knob the upstream client honours.
type NetworkConfig struct {
	DNS           DNSConfig           `yaml:"dns"`
	PreferIPv4    bool                `yaml:"prefer_ipv4"`
	DialTimeoutMs int                 `yaml:"dial_timeout_ms"`
	UpstreamProxy UpstreamProxyConfig `yaml:"upstream_proxy"`
	Circuit       CircuitConfig       `yaml:"circuit"`
	Bandwidth     BandwidthConfig     `yaml:"bandwidth"`
}

// DialTimeout returns the configured dial budget as a time.Duration.
func (n NetworkConfig) DialTimeout() time.Duration {
	return time.Duration(n.DialTimeoutMs) * time.Millisecond
}

// DNSConfig selects the resolver strategy. See docs/network-resilience.md.
type DNSConfig struct {
	Mode            string   `yaml:"mode"`
	Resolvers       []string `yaml:"resolvers"`
	TimeoutMs       int      `yaml:"timeout_ms"`
	CacheTTLs       int      `yaml:"cache_ttl_s"`
	CacheMaxEntries int      `yaml:"cache_max_entries"`
}

// Timeout returns the per-resolver budget as a time.Duration.
func (d DNSConfig) Timeout() time.Duration {
	return time.Duration(d.TimeoutMs) * time.Millisecond
}

// CacheTTL returns the fallback TTL as a time.Duration.
func (d DNSConfig) CacheTTL() time.Duration {
	return time.Duration(d.CacheTTLs) * time.Second
}

// UpstreamProxyConfig wires the upstream HTTP/SOCKS5 proxy chain.
type UpstreamProxyConfig struct {
	Enabled      bool     `yaml:"enabled"`
	URL          string   `yaml:"url"`
	OnlyForHosts []string `yaml:"only_for_hosts"`
}

// CircuitConfig controls the per-host failure breaker.
type CircuitConfig struct {
	Enabled          bool `yaml:"enabled"`
	FailureThreshold int  `yaml:"failure_threshold"`
	CooldownMs       int  `yaml:"cooldown_ms"`
}

// Cooldown returns the configured cool-off as a time.Duration.
func (c CircuitConfig) Cooldown() time.Duration {
	return time.Duration(c.CooldownMs) * time.Millisecond
}

// BandwidthConfig caps the forward path's throughput.
type BandwidthConfig struct {
	ForwardBPS int64 `yaml:"forward_bps"`
	BurstBytes int64 `yaml:"burst_bytes"`
}

// VerifyConfig toggles integrity verification for library files.
type VerifyConfig struct {
	CRC bool `yaml:"crc"`
}

type LogConfig struct {
	Level string `yaml:"level"`
	JSON  bool   `yaml:"json"`
}
