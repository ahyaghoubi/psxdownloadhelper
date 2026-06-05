package netresolve

import (
	"strings"
	"testing"
	"time"
)

func TestNewFromConfigSystemMode(t *testing.T) {
	r, _, err := NewFromConfig(Config{Mode: "system"})
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("returned nil resolver")
	}
}

func TestNewFromConfigDoHMode(t *testing.T) {
	_, _, err := NewFromConfig(Config{
		Mode: "doh",
		Resolvers: []string{
			"https://free.shecan.ir/dns-query",
			"https://1.1.1.1/dns-query",
		},
		Timeout:  500 * time.Millisecond,
		CacheTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewFromConfigDoHRejectsPlainAddress(t *testing.T) {
	_, _, err := NewFromConfig(Config{
		Mode:      "doh",
		Resolvers: []string{"1.1.1.1"},
	})
	if err == nil || !strings.Contains(err.Error(), "https://") {
		t.Errorf("expected https:// error, got %v", err)
	}
}

func TestNewFromConfigMixedMode(t *testing.T) {
	_, _, err := NewFromConfig(Config{
		Mode: "doh+udp",
		Resolvers: []string{
			"https://free.shecan.ir/dns-query",
			"178.22.122.100",
			"8.8.8.8:53",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewFromConfigUnknownMode(t *testing.T) {
	_, _, err := NewFromConfig(Config{Mode: "telepathy"})
	if err == nil || !strings.Contains(err.Error(), "telepathy") {
		t.Errorf("expected unknown-mode error, got %v", err)
	}
}

func TestNewFromConfigDefaultsToSystem(t *testing.T) {
	r, _, err := NewFromConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("nil resolver")
	}
}
