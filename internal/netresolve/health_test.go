package netresolve

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeResolver returns a fixed result after an optional delay, recording how
// many times it was called.
type fakeResolver struct {
	ip    string
	err   error
	delay time.Duration
	calls int
}

func (f *fakeResolver) LookupHost(_ context.Context, _ string) ([]string, time.Duration, error) {
	f.calls++
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.err != nil {
		return nil, 0, f.err
	}
	return []string{f.ip}, time.Minute, nil
}

func TestHealthResolverPrefersHealthy(t *testing.T) {
	dead := &fakeResolver{err: errors.New("i/o timeout")}
	good := &fakeResolver{ip: "1.2.3.4"}

	h := NewHealth(200*time.Millisecond, []string{"dead", "good"}, []Resolver{dead, good})

	// Prime stats: the first call tries dead (fails) then good (succeeds).
	ips, _, err := h.LookupHost(context.Background(), "example.test")
	if err != nil || len(ips) == 0 || ips[0] != "1.2.3.4" {
		t.Fatalf("first lookup: ips=%v err=%v", ips, err)
	}

	// After the failure, "good" should rank ahead of "dead".
	snap := h.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 stats, got %d", len(snap))
	}
	if snap[0].Name != "good" {
		t.Errorf("expected 'good' ranked first after dead failed, got %q (scores: %v / %v)",
			snap[0].Name, snap[0].Score, snap[1].Score)
	}

	// A second lookup should now hit good first; dead's call count must not grow.
	deadCallsBefore := dead.calls
	if _, _, err := h.LookupHost(context.Background(), "example.test"); err != nil {
		t.Fatalf("second lookup failed: %v", err)
	}
	if dead.calls != deadCallsBefore {
		t.Errorf("dead resolver was retried (%d → %d); ranking should skip it",
			deadCallsBefore, dead.calls)
	}
}

func TestHealthResolverFallsBackToTail(t *testing.T) {
	deadA := &fakeResolver{err: errors.New("connection refused")}
	deadB := &fakeResolver{err: errors.New("connection refused")}
	systemTail := &fakeResolver{ip: "9.9.9.9"}

	h := NewHealth(200*time.Millisecond, []string{"a", "b"}, []Resolver{deadA, deadB}, systemTail)

	ips, _, err := h.LookupHost(context.Background(), "example.test")
	if err != nil {
		t.Fatalf("expected tail to answer, got err %v", err)
	}
	if len(ips) == 0 || ips[0] != "9.9.9.9" {
		t.Errorf("expected tail IP 9.9.9.9, got %v", ips)
	}
}

func TestHealthResolverReprobeUpdatesStats(t *testing.T) {
	r := &fakeResolver{ip: "1.1.1.1"}
	h := NewHealth(200*time.Millisecond, []string{"r"}, []Resolver{r})

	before := h.Snapshot()[0].Successes
	h.Reprobe(context.Background(), "example.test")
	after := h.Snapshot()[0].Successes
	if after != before+1 {
		t.Errorf("reprobe should record one success: before=%d after=%d", before, after)
	}
}

func TestNewFromConfigHealthRanking(t *testing.T) {
	_, health, err := NewFromConfig(Config{
		Mode:          "doh+udp",
		Resolvers:     []string{"https://1.1.1.1/dns-query", "8.8.8.8:53"},
		HealthRanking: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if health == nil {
		t.Fatal("expected a HealthResolver when HealthRanking is enabled with resolvers")
	}
	if got := len(health.Snapshot()); got != 2 {
		t.Errorf("expected 2 ranked resolvers, got %d", got)
	}
}

func TestNewFromConfigHealthRankingSystemOnlyIsPlain(t *testing.T) {
	// Health ranking with no configured resolvers (system only) has nothing to
	// rank, so no HealthResolver is returned.
	_, health, err := NewFromConfig(Config{Mode: "system", HealthRanking: true})
	if err != nil {
		t.Fatal(err)
	}
	if health != nil {
		t.Error("system-only config should not produce a HealthResolver")
	}
}
