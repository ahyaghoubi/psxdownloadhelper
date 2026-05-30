// Package config loads and validates psxdh's runtime configuration.
// The on-disk schema is YAML and is documented in plan.md §5.5.
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
	LogIgnored         bool     `yaml:"log_ignored"`
	ExportFormats      []string `yaml:"export_formats"`
	PrefetchSCMetadata bool     `yaml:"prefetch_sc_metadata"`
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
	Mode             string `yaml:"mode"`
	PassthroughHTTPS bool   `yaml:"passthrough_https"`
}

type LogConfig struct {
	Level string `yaml:"level"`
	JSON  bool   `yaml:"json"`
}
