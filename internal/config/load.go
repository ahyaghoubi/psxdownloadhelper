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
// docs/configuration.md. Includes the three fields resolved during
// sequencing (admin.auto_open, library.stable_settle_ms,
// library.ignore_suffixes).
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
		},
		Handoff: HandoffConfig{
			FDM: FDMHandoffConfig{Enabled: true, FallbackToClipboard: true},
		},
		Forward: ForwardConfig{Mode: "auto", PassthroughHTTPS: true},
		Log:     LogConfig{Level: "info", JSON: false},
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
	if !strings.HasPrefix(c.Library.Dir, "~") {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	c.Library.Dir = filepath.Join(home, strings.TrimPrefix(c.Library.Dir, "~"))
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
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level: must be one of debug|info|warn|error, got %q", c.Log.Level)
	}
	return nil
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
