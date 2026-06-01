package netresolve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"
)

// ErrNoResolvers is returned when a MultiResolver is constructed with an
// empty resolver list and asked to resolve anything.
var ErrNoResolvers = errors.New("netresolve: no resolvers configured")

// Resolver returns one or more IP addresses for a hostname. The slice may
// hold a mix of IPv4 and IPv6 addresses; ordering is "best first" — callers
// preferring one family should filter, not re-order.
//
// Implementations must respect ctx for cancellation/deadline. Returning a
// nil error with an empty slice is a programming error; either return ips
// or return a non-nil error.
type Resolver interface {
	// LookupHost resolves host (a name like "gst.prod.dl.playstation.net")
	// into one or more textual IP addresses. If host is already an IP
	// literal, implementations should return it verbatim wrapped in a
	// single-element slice without performing any lookup.
	LookupHost(ctx context.Context, host string) (ips []string, ttl time.Duration, err error)
}

// AsNetResolver is implemented by resolvers that can also be used as the
// stdlib net.Resolver — useful for plugging into a net.Dialer.Resolver.
//
// This is satisfied by SystemResolver only today; others build their own
// Dial path via a custom net.Dialer.Control / DialContext.
type AsNetResolver interface {
	NetResolver() *net.Resolver
}

// MultiResolver tries each inner Resolver in order, advancing only when one
// returns a transient error (timeout, transport failure). NXDOMAIN-style
// "no such host" responses are returned immediately without falling
// through, because trying a second resolver after a clear "the name does
// not exist" answer just hides the truth.
type MultiResolver struct {
	resolvers []Resolver
	// Timeout per-resolver. The MultiResolver also respects ctx; per-
	// resolver timeout is an additional bound.
	Timeout time.Duration
}

// NewMulti wraps the given resolvers. The slice is copied; mutating the
// caller's slice after the constructor returns has no effect.
func NewMulti(timeout time.Duration, resolvers ...Resolver) *MultiResolver {
	rs := make([]Resolver, len(resolvers))
	copy(rs, resolvers)
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &MultiResolver{resolvers: rs, Timeout: timeout}
}

// Len reports the number of resolvers behind the multi.
func (m *MultiResolver) Len() int { return len(m.resolvers) }

// LookupHost tries each underlying resolver in order until one succeeds.
// An error from the last resolver is returned wrapped with ErrAllFailed.
func (m *MultiResolver) LookupHost(ctx context.Context, host string) ([]string, time.Duration, error) {
	if len(m.resolvers) == 0 {
		return nil, 0, ErrNoResolvers
	}
	if ip := parseIPLiteral(host); ip != "" {
		return []string{ip}, 0, nil
	}
	var lastErr error
	for i, r := range m.resolvers {
		rCtx, cancel := context.WithTimeout(ctx, m.Timeout)
		ips, ttl, err := r.LookupHost(rCtx, host)
		cancel()
		if err == nil && len(ips) > 0 {
			return ips, ttl, nil
		}
		if isNXDomain(err) && i == 0 {
			// Authoritative NXDOMAIN should not be papered over.
			return nil, 0, err
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("netresolve: all resolvers returned empty")
	}
	return nil, 0, fmt.Errorf("netresolve: all %d resolvers failed: %w", len(m.resolvers), lastErr)
}

// parseIPLiteral returns the host unchanged if it parses as an IP address,
// otherwise the empty string.
func parseIPLiteral(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return a.String()
	}
	return ""
}

// nxDomainError lets concrete resolvers signal "this name does not exist"
// in a way MultiResolver can detect without importing each transport.
type nxDomainError struct{ host string }

func (e *nxDomainError) Error() string {
	return fmt.Sprintf("netresolve: NXDOMAIN for %q", e.host)
}

// IsNXDomain reports whether err signals an authoritative "no such name"
// response. Useful for callers that want to bypass retry on these.
func IsNXDomain(err error) bool { return isNXDomain(err) }

func isNXDomain(err error) bool {
	if err == nil {
		return false
	}
	var nx *nxDomainError
	return errors.As(err, &nx)
}

// dedupeSort returns ips with duplicates removed and IPv4 addresses
// listed first (so the dialer can fall back to IPv6 only when needed).
func dedupeSort(ips []string) []string {
	seen := make(map[string]struct{}, len(ips))
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := netip.MustParseAddr(out[i]), netip.MustParseAddr(out[j])
		if ai.Is4() != aj.Is4() {
			return ai.Is4()
		}
		return false
	})
	return out
}

// boundedDeadline returns the earlier of ctx's deadline (if any) and now+d.
func boundedDeadline(ctx context.Context, d time.Duration) time.Time {
	if dl, ok := ctx.Deadline(); ok {
		if t := time.Now().Add(d); t.Before(dl) {
			return t
		}
		return dl
	}
	return time.Now().Add(d)
}

// once is a tiny helper for resolvers that need lazy lock initialisation.
// Kept here so each transport file doesn't pull sync into its imports.
var _ = sync.Once{}
