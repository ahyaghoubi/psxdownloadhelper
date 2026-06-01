package circuit

import (
	"errors"
	"testing"
	"time"
)

func TestBreakerStartsClosed(t *testing.T) {
	b := New(Config{FailureThreshold: 3, Cooldown: time.Minute})
	if s := b.State("h"); s != StateClosed {
		t.Errorf("state = %s, want closed", s)
	}
	release, err := b.Allow("h")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	release(nil)
}

func TestBreakerOpensAfterThreshold(t *testing.T) {
	b := New(Config{FailureThreshold: 3, Cooldown: time.Minute})
	for i := 0; i < 3; i++ {
		release, err := b.Allow("h")
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		release(errors.New("boom"))
	}
	if s := b.State("h"); s != StateOpen {
		t.Errorf("state after %d failures = %s, want open", 3, s)
	}
	_, err := b.Allow("h")
	if !errors.Is(err, ErrOpen) {
		t.Errorf("Allow on open breaker err = %v, want ErrOpen", err)
	}
}

func TestBreakerHalfOpensAfterCooldown(t *testing.T) {
	now := time.Now()
	clock := &fakeClock{t: now}
	b := New(Config{FailureThreshold: 2, Cooldown: time.Minute, now: clock.Now})

	// Open the breaker.
	for i := 0; i < 2; i++ {
		release, _ := b.Allow("h")
		release(errors.New("nope"))
	}
	if s := b.State("h"); s != StateOpen {
		t.Fatalf("state = %s", s)
	}

	// Advance past cooldown.
	clock.advance(2 * time.Minute)

	release, err := b.Allow("h")
	if err != nil {
		t.Fatalf("expected probe to be allowed after cooldown: %v", err)
	}
	if s := b.State("h"); s != StateHalfOpen {
		t.Errorf("state during probe = %s, want half-open", s)
	}
	release(nil) // probe succeeds; should close.

	if s := b.State("h"); s != StateClosed {
		t.Errorf("state after successful probe = %s, want closed", s)
	}
}

func TestBreakerReopensOnFailedProbe(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	b := New(Config{FailureThreshold: 1, Cooldown: time.Second, now: clock.Now})

	// Open.
	release, _ := b.Allow("h")
	release(errors.New("nope"))
	if s := b.State("h"); s != StateOpen {
		t.Fatalf("state = %s", s)
	}

	clock.advance(2 * time.Second)
	release, err := b.Allow("h")
	if err != nil {
		t.Fatalf("expected probe allowed: %v", err)
	}
	release(errors.New("still down"))

	if s := b.State("h"); s != StateOpen {
		t.Errorf("state after failed probe = %s, want open", s)
	}
}

func TestBreakerLimitsConcurrentProbes(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	b := New(Config{FailureThreshold: 1, Cooldown: time.Second, HalfOpenMaxProbes: 1, now: clock.Now})

	release, _ := b.Allow("h")
	release(errors.New("nope"))
	clock.advance(2 * time.Second)

	// First probe: allowed.
	r1, err := b.Allow("h")
	if err != nil {
		t.Fatalf("probe 1: %v", err)
	}
	// Second probe before the first releases: refused.
	if _, err := b.Allow("h"); !errors.Is(err, ErrOpen) {
		t.Errorf("probe 2 err = %v, want ErrOpen", err)
	}
	r1(nil)
}

func TestBreakerResetsAllOnEmptyHost(t *testing.T) {
	b := New(Config{FailureThreshold: 1, Cooldown: time.Hour})
	r, _ := b.Allow("a")
	r(errors.New("x"))
	r, _ = b.Allow("b")
	r(errors.New("x"))
	if b.State("a") != StateOpen || b.State("b") != StateOpen {
		t.Fatal("setup")
	}
	b.Reset("")
	if b.State("a") != StateClosed || b.State("b") != StateClosed {
		t.Errorf("Reset(\"\") did not clear all hosts")
	}
}

func TestBreakerEmptyHostBypassesAll(t *testing.T) {
	b := New(Config{FailureThreshold: 1, Cooldown: time.Hour})
	release, err := b.Allow("")
	if err != nil {
		t.Fatal(err)
	}
	release(errors.New("ignored"))
	release, err = b.Allow("") // still allowed
	if err != nil {
		t.Errorf("empty host should always be allowed: %v", err)
	}
	release(nil)
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
