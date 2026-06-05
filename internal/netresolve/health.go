package netresolve

import (
	"context"
	"sort"
	"sync"
	"time"
)

// statResolver decorates a Resolver, recording an exponentially-weighted
// moving average of its latency plus a recent success/failure tally. The
// score() it exposes lets HealthResolver rank a flapping resolver below a
// healthy one without changing the Resolver interface.
type statResolver struct {
	name  string
	inner Resolver

	mu          sync.Mutex
	ewmaMillis  float64 // EWMA of observed latency, milliseconds
	samples     uint64
	successes   uint64
	failures    uint64
	consecFails uint64
}

const ewmaAlpha = 0.3 // weight of the newest sample

func (s *statResolver) LookupHost(ctx context.Context, host string) ([]string, time.Duration, error) {
	start := time.Now()
	ips, ttl, err := s.inner.LookupHost(ctx, host)
	s.record(time.Since(start), err)
	return ips, ttl, err
}

func (s *statResolver) record(elapsed time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms := float64(elapsed) / float64(time.Millisecond)
	if s.samples == 0 {
		s.ewmaMillis = ms
	} else {
		s.ewmaMillis = ewmaAlpha*ms + (1-ewmaAlpha)*s.ewmaMillis
	}
	s.samples++
	// An NXDOMAIN is a valid, fast answer about the *name*, not a fault of the
	// resolver — don't penalise the resolver's health for it.
	if err != nil && !isNXDomain(err) {
		s.failures++
		s.consecFails++
	} else {
		s.successes++
		s.consecFails = 0
	}
}

// score ranks resolvers: lower is better. It is the EWMA latency plus a steep
// penalty per consecutive failure, so a resolver that just timed out drops to
// the back of the queue and recovers as it starts answering again.
func (s *statResolver) score() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.samples == 0 {
		return 0 // unprobed resolvers sort to the front so they get a chance
	}
	return s.ewmaMillis + float64(s.consecFails)*5000.0
}

// ResolverStat is a snapshot of one resolver's health, for the dashboard.
type ResolverStat struct {
	Name        string  `json:"name"`
	Score       float64 `json:"score"`
	EWMAMillis  float64 `json:"ewma_ms"`
	Successes   uint64  `json:"successes"`
	Failures    uint64  `json:"failures"`
	ConsecFails uint64  `json:"consecutive_failures"`
}

func (s *statResolver) snapshot() ResolverStat {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ResolverStat{
		Name:        s.name,
		Score:       s.scoreLocked(),
		EWMAMillis:  s.ewmaMillis,
		Successes:   s.successes,
		Failures:    s.failures,
		ConsecFails: s.consecFails,
	}
}

func (s *statResolver) scoreLocked() float64 {
	if s.samples == 0 {
		return 0
	}
	return s.ewmaMillis + float64(s.consecFails)*5000.0
}

// HealthResolver tries a set of ranked resolvers in health order (best score
// first), then a fixed tail (the system resolver) as a last resort. Stats are
// updated on every lookup, so the ordering self-corrects with live traffic; an
// optional background Reprobe refreshes them when the proxy is idle.
type HealthResolver struct {
	ranked  []*statResolver
	tail    []Resolver
	timeout time.Duration
}

// NewHealth wraps the configured resolvers (ranked by health) and a fixed tail
// of fallback resolvers (typically the system resolver). names labels each
// ranked resolver for the dashboard; it must be the same length as ranked.
func NewHealth(timeout time.Duration, names []string, ranked []Resolver, tail ...Resolver) *HealthResolver {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	stats := make([]*statResolver, len(ranked))
	for i, r := range ranked {
		name := ""
		if i < len(names) {
			name = names[i]
		}
		stats[i] = &statResolver{name: name, inner: r}
	}
	return &HealthResolver{ranked: stats, tail: append([]Resolver(nil), tail...), timeout: timeout}
}

// LookupHost resolves host, trying ranked resolvers best-score-first then the
// tail. It reuses MultiResolver for the actual fallthrough/NXDOMAIN semantics.
func (h *HealthResolver) LookupHost(ctx context.Context, host string) ([]string, time.Duration, error) {
	resolvers := make([]Resolver, 0, len(h.ranked)+len(h.tail))
	for _, s := range h.orderedRanked() {
		resolvers = append(resolvers, s)
	}
	resolvers = append(resolvers, h.tail...)
	return NewMulti(h.timeout, resolvers...).LookupHost(ctx, host)
}

// orderedRanked returns the ranked resolvers sorted by current score.
func (h *HealthResolver) orderedRanked() []*statResolver {
	order := make([]*statResolver, len(h.ranked))
	copy(order, h.ranked)
	sort.SliceStable(order, func(i, j int) bool {
		return order[i].score() < order[j].score()
	})
	return order
}

// Reprobe issues a lookup of host against every ranked resolver to refresh its
// health stats. Intended for a background ticker when there's no live traffic.
func (h *HealthResolver) Reprobe(ctx context.Context, host string) {
	for _, s := range h.ranked {
		rCtx, cancel := context.WithTimeout(ctx, h.timeout)
		_, _, _ = s.LookupHost(rCtx, host)
		cancel()
	}
}

// Snapshot returns the current per-resolver health, best-ranked first. The
// dashboard's connectivity panel renders this.
func (h *HealthResolver) Snapshot() []ResolverStat {
	ordered := h.orderedRanked()
	out := make([]ResolverStat, 0, len(ordered))
	for _, s := range ordered {
		out = append(out, s.snapshot())
	}
	return out
}
