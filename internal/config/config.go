// Package config loads and validates psxdh's runtime configuration.
// The on-disk schema is YAML and is documented in docs/configuration.md.
//
// Every struct field has matching `yaml:` and `json:` tags so the same shape
// round-trips through both formats. The dashboard's config editor uses JSON on
// the wire; the on-disk file is YAML.
package config

import (
	"net"
	"strings"
	"time"
)

type Config struct {
	Proxy      ProxyConfig      `yaml:"proxy" json:"proxy"`
	Admin      AdminConfig      `yaml:"admin" json:"admin"`
	Library    LibraryConfig    `yaml:"library" json:"library"`
	Match      MatchConfig      `yaml:"match" json:"match"`
	Capture    CaptureConfig    `yaml:"capture" json:"capture"`
	Handoff    HandoffConfig    `yaml:"handoff" json:"handoff"`
	Forward    ForwardConfig    `yaml:"forward" json:"forward"`
	Network    NetworkConfig    `yaml:"network" json:"network"`
	Verify     VerifyConfig     `yaml:"verify" json:"verify"`
	MDNS       MDNSConfig       `yaml:"mdns" json:"mdns"`
	Downloader DownloaderConfig `yaml:"downloader" json:"downloader"`
	Cluster    ClusterConfig    `yaml:"cluster" json:"cluster"`
	Jobs       JobsConfig       `yaml:"jobs" json:"jobs"`
	Log        LogConfig        `yaml:"log" json:"log"`
}

type ProxyConfig struct {
	Listen string `yaml:"listen" json:"listen"`
}

// AdminConfig controls the embedded web dashboard / admin HTTP API.
//
// Listen binds the dashboard. When the bound host is not a loopback
// address (i.e. the dashboard is reachable from a phone or another box on
// the LAN) a shared Token is required. An empty Token is auto-generated at
// startup and printed in the banner.
type AdminConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Listen   string `yaml:"listen" json:"listen"`
	Token    string `yaml:"token" json:"token"`
	AutoOpen bool   `yaml:"auto_open" json:"auto_open"`
}

// IsLoopbackBind reports whether the admin listen host is a loopback
// address. A non-loopback bind (LAN-reachable) requires a token. An empty
// or unparseable host (e.g. ":8081") is treated as non-loopback — the safe
// default, since it accepts connections on every interface.
func (a AdminConfig) IsLoopbackBind() bool {
	host, _, err := net.SplitHostPort(a.Listen)
	if err != nil || host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}

type LibraryConfig struct {
	Dir            string   `yaml:"dir" json:"dir"`
	Layout         string   `yaml:"layout" json:"layout"`
	Watch          bool     `yaml:"watch" json:"watch"`
	StableSettleMs int      `yaml:"stable_settle_ms" json:"stable_settle_ms"`
	IgnoreSuffixes []string `yaml:"ignore_suffixes" json:"ignore_suffixes"`
}

func (l LibraryConfig) StableSettle() time.Duration {
	return time.Duration(l.StableSettleMs) * time.Millisecond
}

type MatchConfig struct {
	PS4      bool   `yaml:"ps4" json:"ps4"`
	PS5      bool   `yaml:"ps5" json:"ps5"`
	RulesDir string `yaml:"rules_dir" json:"rules_dir"`
}

type CaptureConfig struct {
	LogIgnored         bool          `yaml:"log_ignored" json:"log_ignored"`
	ExportFormats      []string      `yaml:"export_formats" json:"export_formats"`
	PrefetchSCMetadata bool          `yaml:"prefetch_sc_metadata" json:"prefetch_sc_metadata"`
	Persist            PersistConfig `yaml:"persist" json:"persist"`
}

// PersistConfig controls the append-only JSONL log of capture events.
// When Enabled, the proxy writes one JSON object per line to Path so a
// crash or restart never loses an observed URL.
type PersistConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Path    string `yaml:"path" json:"path"`
	FSync   bool   `yaml:"fsync" json:"fsync"`
}

type FDMHandoffConfig struct {
	Enabled             bool   `yaml:"enabled" json:"enabled"`
	FDMBinary           string `yaml:"fdm_binary" json:"fdm_binary"`
	FallbackToClipboard bool   `yaml:"fallback_to_clipboard" json:"fallback_to_clipboard"`
}

// Aria2HandoffConfig wires the aria2 JSON-RPC handoff. When Enabled, the
// dashboard's "Send to aria2" action (and AutoPush, if set) posts captured
// CDN URLs straight into a running `aria2c --enable-rpc` so the file lands
// in library.dir with its original basename — no copy-paste.
type Aria2HandoffConfig struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	RPCURL    string `yaml:"rpc_url" json:"rpc_url"`
	RPCSecret string `yaml:"rpc_secret" json:"rpc_secret"`
	AutoPush  bool   `yaml:"auto_push" json:"auto_push"`
}

type HandoffConfig struct {
	FDM   FDMHandoffConfig   `yaml:"fdm" json:"fdm"`
	Aria2 Aria2HandoffConfig `yaml:"aria2" json:"aria2"`
}

type ForwardConfig struct {
	Mode             string             `yaml:"mode" json:"mode"`
	PassthroughHTTPS bool               `yaml:"passthrough_https" json:"passthrough_https"`
	Retry            RetryConfig        `yaml:"retry" json:"retry"`
	PartialCache     PartialCacheConfig `yaml:"partial_cache" json:"partial_cache"`
}

// RetryConfig is the backoff policy applied to upstream forward attempts.
// MaxAttempts of 1 disables retry; values <= 0 are normalised to 1.
type RetryConfig struct {
	MaxAttempts      int     `yaml:"max_attempts" json:"max_attempts"`
	InitialBackoffMs int     `yaml:"initial_backoff_ms" json:"initial_backoff_ms"`
	MaxBackoffMs     int     `yaml:"max_backoff_ms" json:"max_backoff_ms"`
	Multiplier       float64 `yaml:"multiplier" json:"multiplier"`
	Jitter           float64 `yaml:"jitter" json:"jitter"`
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
//
// Resume enables cross-run continuation: a `.partial` left behind by a
// dropped forward is continued with a Range request on the next eligible
// forward, gated on the upstream validators (ETag/Last-Modified/size) still
// matching. This is the single biggest win on links that drop mid-transfer.
type PartialCacheConfig struct {
	Enabled      bool  `yaml:"enabled" json:"enabled"`
	MinSizeBytes int64 `yaml:"min_size_bytes" json:"min_size_bytes"`
	Resume       bool  `yaml:"resume" json:"resume"`
}

// NetworkConfig groups every knob the upstream client honours.
type NetworkConfig struct {
	DNS           DNSConfig           `yaml:"dns" json:"dns"`
	PreferIPv4    bool                `yaml:"prefer_ipv4" json:"prefer_ipv4"`
	DialTimeoutMs int                 `yaml:"dial_timeout_ms" json:"dial_timeout_ms"`
	UpstreamProxy UpstreamProxyConfig `yaml:"upstream_proxy" json:"upstream_proxy"`
	Circuit       CircuitConfig       `yaml:"circuit" json:"circuit"`
	Bandwidth     BandwidthConfig     `yaml:"bandwidth" json:"bandwidth"`
}

// DialTimeout returns the configured dial budget as a time.Duration.
func (n NetworkConfig) DialTimeout() time.Duration {
	return time.Duration(n.DialTimeoutMs) * time.Millisecond
}

// DNSConfig selects the resolver strategy. See docs/network-resilience.md.
type DNSConfig struct {
	Mode            string          `yaml:"mode" json:"mode"`
	Resolvers       []string        `yaml:"resolvers" json:"resolvers"`
	TimeoutMs       int             `yaml:"timeout_ms" json:"timeout_ms"`
	CacheTTLs       int             `yaml:"cache_ttl_s" json:"cache_ttl_s"`
	CacheMaxEntries int             `yaml:"cache_max_entries" json:"cache_max_entries"`
	Health          DNSHealthConfig `yaml:"health" json:"health"`
}

// DNSHealthConfig re-ranks the resolver list by observed latency and recent
// success so a dead first entry (common with Iranian DoH endpoints that
// flap) stops taxing every lookup. Ordering only — the resolver set and the
// system fallback are unchanged.
type DNSHealthConfig struct {
	Enabled           bool `yaml:"enabled" json:"enabled"`
	ReprobeIntervalMs int  `yaml:"reprobe_interval_ms" json:"reprobe_interval_ms"`
}

// ReprobeInterval returns the background re-probe cadence as a Duration.
func (h DNSHealthConfig) ReprobeInterval() time.Duration {
	return time.Duration(h.ReprobeIntervalMs) * time.Millisecond
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
	Enabled      bool     `yaml:"enabled" json:"enabled"`
	URL          string   `yaml:"url" json:"url"`
	OnlyForHosts []string `yaml:"only_for_hosts" json:"only_for_hosts"`
}

// CircuitConfig controls the per-host failure breaker.
type CircuitConfig struct {
	Enabled          bool `yaml:"enabled" json:"enabled"`
	FailureThreshold int  `yaml:"failure_threshold" json:"failure_threshold"`
	CooldownMs       int  `yaml:"cooldown_ms" json:"cooldown_ms"`
}

// Cooldown returns the configured cool-off as a time.Duration.
func (c CircuitConfig) Cooldown() time.Duration {
	return time.Duration(c.CooldownMs) * time.Millisecond
}

// BandwidthConfig caps the forward path's throughput.
type BandwidthConfig struct {
	ForwardBPS int64 `yaml:"forward_bps" json:"forward_bps"`
	BurstBytes int64 `yaml:"burst_bytes" json:"burst_bytes"`
}

// VerifyConfig toggles integrity verification for library files.
//
// CRC enables `.crc` sidecar verification. OnStable runs that verification
// when the watcher promotes a file (KindStable) so a corrupt PKG is never
// marked served. RequireSizeMatch refuses to serve a file whose on-disk size
// differs from the upstream Content-Length captured for its URL — the cheap
// guarantee that holds even without a `.crc`.
type VerifyConfig struct {
	CRC              bool `yaml:"crc" json:"crc"`
	OnStable         bool `yaml:"on_stable" json:"on_stable"`
	RequireSizeMatch bool `yaml:"require_size_match" json:"require_size_match"`
}

// MDNSConfig advertises psxdh on the LAN via mDNS (_http._tcp) so the
// console-setup step doesn't require hunting for the PC's IP. See ADR 0004.
type MDNSConfig struct {
	Enabled      bool   `yaml:"enabled" json:"enabled"`
	InstanceName string `yaml:"instance_name" json:"instance_name"`
}

// DownloaderConfig controls the embedded downloader (a managed aria2c
// subprocess). See ADR 0005. Used by cluster nodes (and optionally a single
// node) to fetch PKG parts directly instead of handing off to an external tool.
type DownloaderConfig struct {
	Engine               string `yaml:"engine" json:"engine"`                                 // "aria2" (only engine for now)
	Aria2Binary          string `yaml:"aria2_binary" json:"aria2_binary"`                     // auto-detect on PATH when empty
	RPCPort              int    `yaml:"rpc_port" json:"rpc_port"`                             // aria2 RPC listen port
	RPCSecret            string `yaml:"rpc_secret" json:"rpc_secret"`                         // auto-generated when empty
	ConnectionsPerServer int    `yaml:"connections_per_server" json:"connections_per_server"` // aria2 -x
	Split                int    `yaml:"split" json:"split"`                                   // aria2 -s
	MaxConcurrent        int    `yaml:"max_concurrent" json:"max_concurrent"`                 // aria2 -j
	// AllowHTTPFallback uses the built-in HTTP engine when aria2c is missing.
	// Default false: cluster nodes require aria2c. Set true only for dev/CI.
	AllowHTTPFallback bool `yaml:"allow_http_fallback" json:"allow_http_fallback"`
}

// ClusterConfig wires the master/slave distributed-download cluster. See
// ADR 0005. The master is the node the PS5 proxies through; slaves are extra
// machines that download assigned parts and hand them back to the master.
type ClusterConfig struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	Role      string `yaml:"role" json:"role"`             // "master" | "slave"
	NodeName  string `yaml:"node_name" json:"node_name"`   // defaults to hostname when empty
	Bind      string `yaml:"bind" json:"bind"`             // host:port for the cluster/agent API
	MasterURL string `yaml:"master_url" json:"master_url"` // slave: base URL of the master (for pushing parts)
	Token     string `yaml:"token" json:"token"`           // shared cluster auth; generated on master when empty
	// MasterAsNode makes the master participate as a download worker too. When
	// enabled, the master starts the embedded downloader and downloads assigned
	// parts directly into library.dir (no loopback HTTP).
	MasterAsNode bool `yaml:"master_as_node" json:"master_as_node"`
}

// JobsConfig controls the portable capture-jobs workflow: importing a JSONL
// capture log produced on a different machine, optionally enumerating the
// full PKG part series via the CDN, and persisting cluster job state across
// restarts. See README ("Capture at home, download at work") and
// docs/configuration.md for the recommended profiles.
type JobsConfig struct {
	// ImportOnStart, when set, points to a JSONL capture file that the master
	// imports immediately after startup. Useful for the work profile where
	// the file was produced at home.
	ImportOnStart string `yaml:"import_on_start" json:"import_on_start"`
	// ImportEnumerate triggers cluster.Enumerate for each imported title so
	// the work network can fill in any missing _N parts the home capture
	// missed. Disable when the work network can't reach the PlayStation CDN.
	ImportEnumerate bool `yaml:"import_enumerate" json:"import_enumerate"`
	// StatePath is where the cluster snapshots resumable job state to disk.
	// Empty disables persistence (the in-memory behaviour of earlier
	// releases).
	StatePath string `yaml:"state_path" json:"state_path"`
}

type LogConfig struct {
	Level string `yaml:"level" json:"level"`
	JSON  bool   `yaml:"json" json:"json"`
}
