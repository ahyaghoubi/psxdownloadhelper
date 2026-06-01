// Package circuit implements a per-host circuit breaker used by the
// upstream forward path. When a host fails N times in a row, the
// breaker opens and short-circuits subsequent requests with a fail-fast
// error until a cooldown elapses. Then it half-opens, lets one probe
// through, and either re-closes on success or re-opens on failure.
//
// The breaker is a cooperative back-pressure tool: it prevents the
// proxy from hammering a dead host while the upstream is genuinely
// down. It does not replace retry — they compose.
package circuit

import (
	"errors"
	"sync"
	"time"
)

// State is the circuit's current state for a given host.
type State int

const (
	// StateClosed means traffic flows normally.
	StateClosed State = iota
	// StateOpen means requests fail fast.
	StateOpen
	// StateHalfOpen means a single probe is allowed through to test
	// whether the host has recovered.
	StateHalfOpen
)

// String renders the state for logs / diagnostics.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// ErrOpen is returned by Allow when the breaker is open for that host.
var ErrOpen = errors.New("circuit: breaker is open")

// Config controls the breaker's thresholds and timing.
type Config struct {
	// FailureThreshold is the consecutive-failure count that opens the
	// breaker. Values <= 0 are treated as 5.
	FailureThreshold int
	// Cooldown is how long the breaker stays open before half-opening.
	// Values <= 0 are treated as 30s.
	Cooldown time.Duration
	// HalfOpenMaxProbes is the number of concurrent probes allowed in
	// the half-open state. Values <= 0 are treated as 1.
	HalfOpenMaxProbes int
	// now lets tests inject a clock.
	now func() time.Time
}

// Breaker is a thread-safe collection of per-host states.
type Breaker struct {
	cfg Config
	mu  sync.Mutex
	m   map[string]*hostState
}

type hostState struct {
	state        State
	failures     int
	openedAt     time.Time
	activeProbes int
}

// New returns a Breaker with cfg's policy.
func New(cfg Config) *Breaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Second
	}
	if cfg.HalfOpenMaxProbes <= 0 {
		cfg.HalfOpenMaxProbes = 1
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &Breaker{cfg: cfg, m: make(map[string]*hostState)}
}

// Allow checks whether a request for host should proceed. The returned
// release function MUST be called (in either branch — success or
// failure) so the breaker can update its counters.
//
//	release, err := b.Allow("gst.prod.dl.playstation.net")
//	if err != nil { ... }
//	defer release(err)
func (b *Breaker) Allow(host string) (release func(error), err error) {
	if host == "" {
		// No host → no tracking, no breaker.
		return func(error) {}, nil
	}
	b.mu.Lock()
	s := b.get(host)
	now := b.cfg.now()

	switch s.state {
	case StateOpen:
		if now.Sub(s.openedAt) >= b.cfg.Cooldown {
			s.state = StateHalfOpen
			s.activeProbes = 0
		} else {
			b.mu.Unlock()
			return nil, ErrOpen
		}
	}
	if s.state == StateHalfOpen && s.activeProbes >= b.cfg.HalfOpenMaxProbes {
		b.mu.Unlock()
		return nil, ErrOpen
	}
	if s.state == StateHalfOpen {
		s.activeProbes++
	}
	b.mu.Unlock()

	return func(opErr error) {
		b.record(host, opErr)
	}, nil
}

// State returns the current state for host (StateClosed if never seen).
func (b *Breaker) State(host string) State {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.m[host]; ok {
		return s.state
	}
	return StateClosed
}

// Reset clears the breaker state for host (or all hosts when host == "").
func (b *Breaker) Reset(host string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if host == "" {
		b.m = make(map[string]*hostState)
		return
	}
	delete(b.m, host)
}

func (b *Breaker) get(host string) *hostState {
	s, ok := b.m[host]
	if !ok {
		s = &hostState{}
		b.m[host] = s
	}
	return s
}

// record applies the outcome of an Allow'd request to host's state.
func (b *Breaker) record(host string, opErr error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.get(host)
	if s.state == StateHalfOpen && s.activeProbes > 0 {
		s.activeProbes--
	}
	if opErr == nil {
		// Success: close the circuit and reset counters.
		s.state = StateClosed
		s.failures = 0
		s.openedAt = time.Time{}
		return
	}
	switch s.state {
	case StateClosed:
		s.failures++
		if s.failures >= b.cfg.FailureThreshold {
			s.state = StateOpen
			s.openedAt = b.cfg.now()
		}
	case StateHalfOpen:
		// Failed probe → re-open.
		s.state = StateOpen
		s.openedAt = b.cfg.now()
	case StateOpen:
		// Should not happen (Allow would have blocked) but be defensive.
		s.openedAt = b.cfg.now()
	}
}
