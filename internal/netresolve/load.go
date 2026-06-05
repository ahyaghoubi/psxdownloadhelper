package netresolve

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// Config is the netresolve subset of the user's runtime config.
// It mirrors the YAML schema documented in docs/configuration.md.
type Config struct {
	// Mode selects the resolver strategy:
	//   "system"  — net.DefaultResolver only.
	//   "udp"     — Resolvers as plain DNS/53. Falls back to system if empty.
	//   "doh"     — Resolvers as DoH endpoints. Falls back to system if empty.
	//   "doh+udp" — Try every "https://" entry as DoH, every other entry as UDP, then system.
	Mode string
	// Resolvers are addresses or URLs. Entries starting with "https://" are
	// treated as DoH endpoints; everything else as a UDP server "host[:53]".
	Resolvers []string
	// Timeout is the per-resolver query budget. Zero means 1500ms.
	Timeout time.Duration
	// CacheTTL is the fallback TTL when an inner resolver returns zero
	// (e.g. SystemResolver). Zero means 5 minutes.
	CacheTTL time.Duration
	// CacheMaxEntries caps the cache size. Zero means 4096.
	CacheMaxEntries int
	// HealthRanking, when true, ranks the configured resolvers by observed
	// latency/success so a flapping endpoint stops taxing every lookup. The
	// system resolver remains a fixed last-resort tail. See health.go.
	HealthRanking bool
}

// NewFromConfig builds a resolver chain matching cfg. The returned resolver
// is always wrapped in a cache. If cfg.Mode is empty or "system", the chain
// degenerates to "system resolver + cache".
//
// The second return value is the *HealthResolver when health ranking is active
// (else nil), so callers can expose its Snapshot/Reprobe to the dashboard.
func NewFromConfig(cfg Config) (Resolver, *HealthResolver, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" {
		mode = "system"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}

	// ranked holds the user-configured resolvers (with display names); the
	// system resolver is always appended as the fixed fallback tail.
	var ranked []Resolver
	var names []string
	add := func(r Resolver, name string) {
		ranked = append(ranked, r)
		names = append(names, name)
	}

	switch mode {
	case "system":
		// Nothing to rank — system only.
	case "udp":
		for _, r := range cfg.Resolvers {
			if r = strings.TrimSpace(r); r != "" {
				add(NewUDP(r), r)
			}
		}
	case "doh":
		for _, r := range cfg.Resolvers {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if !strings.HasPrefix(strings.ToLower(r), "https://") {
				return nil, nil, fmt.Errorf("netresolve: doh mode requires https:// endpoints, got %q", r)
			}
			add(NewDoH(r), r)
		}
	case "doh+udp":
		for _, r := range cfg.Resolvers {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(r), "https://") {
				add(NewDoH(r), r)
			} else {
				add(NewUDP(r), r)
			}
		}
	default:
		return nil, nil, fmt.Errorf("netresolve: unknown dns mode %q (want system|udp|doh|doh+udp)", mode)
	}

	system := NewSystem()
	if cfg.HealthRanking && len(ranked) > 0 {
		h := NewHealth(timeout, names, ranked, system)
		return NewCache(h, cfg.CacheTTL, cfg.CacheMaxEntries), h, nil
	}

	inner := append(ranked, system)
	multi := NewMulti(timeout, inner...)
	return NewCache(multi, cfg.CacheTTL, cfg.CacheMaxEntries), nil, nil
}

// HostPort splits a "host[:port]" address; missing port returns 0.
// Exported because the doctor command formats addresses the same way.
func HostPort(addr string) (host string, port int) {
	if h, p, err := net.SplitHostPort(addr); err == nil {
		host = h
		_, _ = fmt.Sscanf(p, "%d", &port)
		return
	}
	return addr, 0
}
