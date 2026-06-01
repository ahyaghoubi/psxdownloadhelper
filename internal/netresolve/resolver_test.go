package netresolve

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// stubResolver lets tests pre-program a single response.
type stubResolver struct {
	ips   []string
	ttl   time.Duration
	err   error
	calls atomic.Int32
}

func (s *stubResolver) LookupHost(_ context.Context, _ string) ([]string, time.Duration, error) {
	s.calls.Add(1)
	return s.ips, s.ttl, s.err
}

func TestMultiResolverSucceedsOnFirst(t *testing.T) {
	a := &stubResolver{ips: []string{"1.2.3.4"}}
	b := &stubResolver{err: errors.New("should not be called")}
	m := NewMulti(time.Second, a, b)

	ips, _, err := m.LookupHost(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("LookupHost: %v", err)
	}
	if len(ips) != 1 || ips[0] != "1.2.3.4" {
		t.Errorf("ips = %v", ips)
	}
	if a.calls.Load() != 1 || b.calls.Load() != 0 {
		t.Errorf("calls: a=%d b=%d", a.calls.Load(), b.calls.Load())
	}
}

func TestMultiResolverFallsThroughTransientError(t *testing.T) {
	a := &stubResolver{err: errors.New("transient")}
	b := &stubResolver{ips: []string{"5.6.7.8"}}
	m := NewMulti(time.Second, a, b)

	ips, _, err := m.LookupHost(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("LookupHost: %v", err)
	}
	if len(ips) != 1 || ips[0] != "5.6.7.8" {
		t.Errorf("ips = %v", ips)
	}
}

func TestMultiResolverHonoursNXDOMAIN(t *testing.T) {
	a := &stubResolver{err: &nxDomainError{host: "x"}}
	b := &stubResolver{ips: []string{"1.1.1.1"}}
	m := NewMulti(time.Second, a, b)

	_, _, err := m.LookupHost(context.Background(), "x")
	if !IsNXDomain(err) {
		t.Errorf("expected NXDOMAIN, got %v", err)
	}
	if b.calls.Load() != 0 {
		t.Errorf("second resolver should not be called on authoritative NXDOMAIN")
	}
}

func TestMultiResolverPassesIPLiteralThrough(t *testing.T) {
	a := &stubResolver{err: errors.New("should not be called")}
	m := NewMulti(time.Second, a)

	ips, _, err := m.LookupHost(context.Background(), "8.8.4.4")
	if err != nil {
		t.Fatalf("LookupHost literal: %v", err)
	}
	if len(ips) != 1 || ips[0] != "8.8.4.4" {
		t.Errorf("literal not preserved: %v", ips)
	}
	if a.calls.Load() != 0 {
		t.Error("resolver was called for an IP literal")
	}
}

func TestMultiResolverNoResolvers(t *testing.T) {
	m := NewMulti(time.Second)
	_, _, err := m.LookupHost(context.Background(), "example.com")
	if !errors.Is(err, ErrNoResolvers) {
		t.Errorf("err = %v, want ErrNoResolvers", err)
	}
}

func TestDedupeSortIPv4First(t *testing.T) {
	got := dedupeSort([]string{
		"2001:db8::1",
		"1.1.1.1",
		"1.1.1.1",
		"2.2.2.2",
		"2001:db8::1",
	})
	want := []string{"1.1.1.1", "2.2.2.2", "2001:db8::1"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
