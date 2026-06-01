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
}

// NewFromConfig builds a resolver chain matching cfg. The returned resolver
// is always wrapped in a cache. If cfg.Mode is empty or "system", the chain
// degenerates to "system resolver + cache".
func NewFromConfig(cfg Config) (Resolver, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" {
		mode = "system"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}

	var inner []Resolver
	switch mode {
	case "system":
		inner = []Resolver{NewSystem()}
	case "udp":
		for _, r := range cfg.Resolvers {
			if r = strings.TrimSpace(r); r != "" {
				inner = append(inner, NewUDP(r))
			}
		}
		inner = append(inner, NewSystem())
	case "doh":
		for _, r := range cfg.Resolvers {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if !strings.HasPrefix(strings.ToLower(r), "https://") {
				return nil, fmt.Errorf("netresolve: doh mode requires https:// endpoints, got %q", r)
			}
			inner = append(inner, NewDoH(r))
		}
		inner = append(inner, NewSystem())
	case "doh+udp":
		for _, r := range cfg.Resolvers {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(r), "https://") {
				inner = append(inner, NewDoH(r))
			} else {
				inner = append(inner, NewUDP(r))
			}
		}
		inner = append(inner, NewSystem())
	default:
		return nil, fmt.Errorf("netresolve: unknown dns mode %q (want system|udp|doh|doh+udp)", mode)
	}

	multi := NewMulti(timeout, inner...)
	return NewCache(multi, cfg.CacheTTL, cfg.CacheMaxEntries), nil
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
