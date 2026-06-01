package netresolve

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCacheHitsAvoidInnerCall(t *testing.T) {
	inner := &stubResolver{ips: []string{"9.9.9.9"}, ttl: time.Minute}
	c := NewCache(inner, time.Minute, 16)

	for i := 0; i < 5; i++ {
		ips, _, err := c.LookupHost(context.Background(), "host.example")
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if ips[0] != "9.9.9.9" {
			t.Errorf("ips = %v", ips)
		}
	}
	if inner.calls.Load() != 1 {
		t.Errorf("inner calls = %d, want 1", inner.calls.Load())
	}
}

func TestCacheExpiresPastTTL(t *testing.T) {
	inner := &stubResolver{ips: []string{"1.1.1.1"}, ttl: 50 * time.Millisecond}
	c := NewCache(inner, 50*time.Millisecond, 16)

	if _, _, err := c.LookupHost(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, _, err := c.LookupHost(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if inner.calls.Load() != 2 {
		t.Errorf("expected 2 inner calls after TTL expiry, got %d", inner.calls.Load())
	}
}

func TestCachePropagatesError(t *testing.T) {
	inner := &stubResolver{err: errors.New("boom")}
	c := NewCache(inner, time.Minute, 16)

	_, _, err := c.LookupHost(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error")
	}
	if c.Len() != 0 {
		t.Errorf("cache should not store errors, len = %d", c.Len())
	}
}

func TestCacheUsesDefaultTTLWhenInnerReturnsZero(t *testing.T) {
	inner := &stubResolver{ips: []string{"1.2.3.4"}, ttl: 0}
	c := NewCache(inner, 200*time.Millisecond, 16)

	if _, _, err := c.LookupHost(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	// Within the default TTL: still a hit.
	time.Sleep(50 * time.Millisecond)
	if _, _, err := c.LookupHost(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if inner.calls.Load() != 1 {
		t.Errorf("inner calls = %d, want 1", inner.calls.Load())
	}
	// Past default TTL.
	time.Sleep(200 * time.Millisecond)
	if _, _, err := c.LookupHost(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if inner.calls.Load() != 2 {
		t.Errorf("inner calls after default TTL = %d, want 2", inner.calls.Load())
	}
}

func TestCachePurge(t *testing.T) {
	inner := &stubResolver{ips: []string{"1.1.1.1"}, ttl: time.Hour}
	c := NewCache(inner, time.Hour, 16)
	_, _, _ = c.LookupHost(context.Background(), "x")
	if c.Len() != 1 {
		t.Fatalf("len = %d", c.Len())
	}
	c.Purge()
	if c.Len() != 0 {
		t.Errorf("len after Purge = %d", c.Len())
	}
}

func TestCacheEvictsWhenFull(t *testing.T) {
	inner := &stubResolver{ips: []string{"1.1.1.1"}, ttl: time.Hour}
	c := NewCache(inner, time.Hour, 2)
	for _, h := range []string{"a", "b", "c"} {
		if _, _, err := c.LookupHost(context.Background(), h); err != nil {
			t.Fatal(err)
		}
	}
	if c.Len() > 2 {
		t.Errorf("cache exceeded maxEntries: len = %d", c.Len())
	}
}
