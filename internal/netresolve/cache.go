package netresolve

import (
	"context"
	"sync"
	"time"
)

// CachingResolver wraps any Resolver with an in-memory TTL cache. Lookups
// for the same host within the TTL window short-circuit to the cached
// answer; expired entries fall through to the inner resolver and the
// answer is re-cached.
//
// The cache is bounded by MaxEntries; the eviction policy is "drop a
// random expired entry, else drop the oldest" (no full LRU — keeps the
// implementation tiny, and DNS workloads don't have the access skew that
// makes LRU shine).
type CachingResolver struct {
	inner      Resolver
	defaultTTL time.Duration
	maxEntries int

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	ips     []string
	expires time.Time
}

// NewCache wraps inner with a cache. defaultTTL is used when the inner
// resolver returns a zero TTL (e.g. SystemResolver). maxEntries caps the
// in-memory size; pass <= 0 for the package default (4096).
func NewCache(inner Resolver, defaultTTL time.Duration, maxEntries int) *CachingResolver {
	if defaultTTL <= 0 {
		defaultTTL = 5 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = 4096
	}
	return &CachingResolver{
		inner:      inner,
		defaultTTL: defaultTTL,
		maxEntries: maxEntries,
		entries:    make(map[string]cacheEntry),
	}
}

// LookupHost implements Resolver, returning the cached value when fresh.
//
// The returned TTL is the *remaining* cache lifetime, not the original
// upstream TTL. This is the value the dialer cares about ("how long can I
// keep using this IP without re-asking?").
func (c *CachingResolver) LookupHost(ctx context.Context, host string) ([]string, time.Duration, error) {
	if ip := parseIPLiteral(host); ip != "" {
		return []string{ip}, 0, nil
	}
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.entries[host]; ok && now.Before(e.expires) {
		ips := append([]string(nil), e.ips...)
		ttl := e.expires.Sub(now)
		c.mu.Unlock()
		return ips, ttl, nil
	}
	c.mu.Unlock()

	ips, ttl, err := c.inner.LookupHost(ctx, host)
	if err != nil {
		return nil, 0, err
	}
	if ttl <= 0 {
		ttl = c.defaultTTL
	}
	c.put(host, ips, now.Add(ttl))
	return ips, ttl, nil
}

func (c *CachingResolver) put(host string, ips []string, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.maxEntries {
		c.evictLocked()
	}
	c.entries[host] = cacheEntry{ips: append([]string(nil), ips...), expires: expires}
}

// evictLocked drops the first expired entry the map iterator yields, or —
// if all entries are still fresh — the entry with the soonest expiry.
// Caller must hold c.mu.
func (c *CachingResolver) evictLocked() {
	now := time.Now()
	for k, e := range c.entries {
		if e.expires.Before(now) {
			delete(c.entries, k)
			return
		}
	}
	// All fresh; drop the soonest-to-expire.
	var (
		dropKey  string
		dropExpr time.Time
	)
	for k, e := range c.entries {
		if dropKey == "" || e.expires.Before(dropExpr) {
			dropKey = k
			dropExpr = e.expires
		}
	}
	if dropKey != "" {
		delete(c.entries, dropKey)
	}
}

// Purge drops every entry. Used by `psxdh doctor` to force a fresh probe.
func (c *CachingResolver) Purge() {
	c.mu.Lock()
	c.entries = make(map[string]cacheEntry)
	c.mu.Unlock()
}

// Len reports the current number of cached entries (fresh + stale).
func (c *CachingResolver) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
