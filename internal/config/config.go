// Package config loads psxdh settings. Everything has a working default, so a
// config file is optional. The YAML keys match older psxdh releases, so an
// existing ~/.config/psxdh/config.yaml keeps working; keys for removed
// features are ignored.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the full psxdh configuration.
type Config struct {
	Proxy struct {
		// Listen is the address the console points its proxy at. The
		// dashboard is served on the same address.
		Listen string `yaml:"listen"`
	} `yaml:"proxy"`

	Library struct {
		// Dir is where your download manager saves PKG files.
		Dir string `yaml:"dir"`
		// IgnoreSuffixes mark files a download manager is still writing.
		IgnoreSuffixes []string `yaml:"ignore_suffixes"`
	} `yaml:"library"`

	Network struct {
		// UpstreamProxy optionally routes forwarded HTTP traffic through
		// another proxy (http://, https:// or socks5:// URL).
		UpstreamProxy struct {
			Enabled bool   `yaml:"enabled"`
			URL     string `yaml:"url"`
		} `yaml:"upstream_proxy"`
	} `yaml:"network"`

	Log struct {
		Level string `yaml:"level"`
	} `yaml:"log"`
}

// DefaultIgnoreSuffixes covers the temporary names used by common download
// managers and browsers.
var DefaultIgnoreSuffixes = []string{".part", ".fdmdownload", ".tmp", ".crdownload", ".aria2", ".download"}

// Default returns the zero-config settings.
func Default() *Config {
	c := &Config{}
	c.Proxy.Listen = "0.0.0.0:8080"
	c.Library.Dir = "~/Downloads/psxdh"
	c.Library.IgnoreSuffixes = append([]string(nil), DefaultIgnoreSuffixes...)
	c.Log.Level = "info"
	return c
}

// DefaultPath is the config file psxdh reads when --config is not given.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "psxdh", "config.yaml")
}

// Load reads path over the defaults. An empty path returns the defaults, and
// so does a missing file at the default location.
func Load(path string, explicit bool) (*Config, error) {
	c := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(data, c); err != nil {
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}
		case errors.Is(err, os.ErrNotExist) && !explicit:
			// No config file yet: defaults are fine.
		default:
			return nil, fmt.Errorf("read config: %w", err)
		}
	}
	return c, nil
}

// Finalise expands ~ in paths and checks the values. Call it after applying
// command-line overrides.
func (c *Config) Finalise() error {
	dir, err := expandHome(c.Library.Dir)
	if err != nil {
		return err
	}
	if c.Library.Dir, err = filepath.Abs(dir); err != nil {
		return fmt.Errorf("library dir: %w", err)
	}
	if _, _, err := net.SplitHostPort(c.Proxy.Listen); err != nil {
		return fmt.Errorf("proxy.listen %q: use host:port, for example 0.0.0.0:8080", c.Proxy.Listen)
	}
	if up := c.Network.UpstreamProxy; up.Enabled {
		u, err := url.Parse(up.URL)
		if err != nil || u.Host == "" {
			return fmt.Errorf("network.upstream_proxy.url %q is not a valid URL", up.URL)
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("network.upstream_proxy.url: scheme %q not supported (use http, https or socks5)", u.Scheme)
		}
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	case "":
		c.Log.Level = "info"
	default:
		return fmt.Errorf("log.level %q: use debug, info, warn or error", c.Log.Level)
	}
	return nil
}

// UpstreamProxyURL returns the parsed upstream proxy, or nil when disabled.
func (c *Config) UpstreamProxyURL() *url.URL {
	if !c.Network.UpstreamProxy.Enabled {
		return nil
	}
	u, _ := url.Parse(c.Network.UpstreamProxy.URL)
	return u
}

func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", p, err)
	}
	return filepath.Join(home, p[1:]), nil
}
