package netresolve

import (
	"context"
	"net"
	"time"
)

// SystemResolver delegates to net.DefaultResolver — i.e. it uses whatever
// the operating system (/etc/resolv.conf on Linux, the registry on
// Windows, scutil on macOS) is configured to use.
//
// It is the fallback-of-last-resort in a MultiResolver chain when the
// user has not explicitly configured anything else.
type SystemResolver struct {
	r *net.Resolver
}

// NewSystem returns a resolver that uses the host's configured resolver.
func NewSystem() *SystemResolver {
	return &SystemResolver{r: net.DefaultResolver}
}

// LookupHost implements Resolver.
//
// The system resolver does not expose TTLs to user space (POSIX does not
// require it); we return 0 so the cache layer falls back to its default
// TTL when wrapping a SystemResolver.
func (s *SystemResolver) LookupHost(ctx context.Context, host string) ([]string, time.Duration, error) {
	if ip := parseIPLiteral(host); ip != "" {
		return []string{ip}, 0, nil
	}
	ips, err := s.r.LookupHost(ctx, host)
	if err != nil {
		if isStdlibNotFound(err) {
			return nil, 0, &nxDomainError{host: host}
		}
		return nil, 0, err
	}
	return dedupeSort(ips), 0, nil
}

// NetResolver satisfies AsNetResolver, so a SystemResolver can be plugged
// into a net.Dialer.Resolver field directly.
func (s *SystemResolver) NetResolver() *net.Resolver { return s.r }

// isStdlibNotFound recognises the stdlib's "no such host" *net.DNSError.
func isStdlibNotFound(err error) bool {
	type notFounder interface{ IsNotFound() bool }
	if e, ok := err.(notFounder); ok && e.IsNotFound() {
		return true
	}
	return false
}
