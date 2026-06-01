package retry

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPolicySucceedsOnFirstAttempt(t *testing.T) {
	p := Policy{MaxAttempts: 3, InitialBackoff: time.Millisecond}
	var calls atomic.Int32
	op := Op(func(_ int) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	resp, err := p.Do(context.Background(), DefaultClassifier, op)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestPolicyRetriesTransientThenSucceeds(t *testing.T) {
	p := Policy{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}
	p.SetRand(rand.New(rand.NewSource(1)))

	var calls atomic.Int32
	op := Op(func(_ int) (*http.Response, error) {
		n := calls.Add(1)
		if n < 3 {
			return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	resp, err := p.Do(context.Background(), DefaultClassifier, op)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestPolicyRespectsMaxAttempts(t *testing.T) {
	p := Policy{MaxAttempts: 2, InitialBackoff: time.Millisecond}
	p.SetRand(rand.New(rand.NewSource(1)))
	var calls atomic.Int32
	op := Op(func(_ int) (*http.Response, error) {
		calls.Add(1)
		return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	})
	_, err := p.Do(context.Background(), DefaultClassifier, op)
	if err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestPolicyDoesNotRetryNonTransient(t *testing.T) {
	p := Policy{MaxAttempts: 5, InitialBackoff: time.Millisecond}
	var calls atomic.Int32
	op := Op(func(_ int) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("definitely not a network error")
	})
	_, err := p.Do(context.Background(), DefaultClassifier, op)
	if err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestPolicyRetries5xxButNot4xx(t *testing.T) {
	if !DefaultClassifier(nil, &http.Response{StatusCode: 502}) {
		t.Error("502 should be retried")
	}
	if !DefaultClassifier(nil, &http.Response{StatusCode: 503}) {
		t.Error("503 should be retried")
	}
	if !DefaultClassifier(nil, &http.Response{StatusCode: 504}) {
		t.Error("504 should be retried")
	}
	if DefaultClassifier(nil, &http.Response{StatusCode: 404}) {
		t.Error("404 should not be retried")
	}
	if DefaultClassifier(nil, &http.Response{StatusCode: 200}) {
		t.Error("200 should not be retried")
	}
}

func TestPolicyHonoursContextCancel(t *testing.T) {
	p := Policy{MaxAttempts: 10, InitialBackoff: 500 * time.Millisecond}
	p.SetRand(rand.New(rand.NewSource(1)))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var calls atomic.Int32
	op := Op(func(_ int) (*http.Response, error) {
		calls.Add(1)
		return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	})
	start := time.Now()
	_, err := p.Do(ctx, DefaultClassifier, op)
	if err == nil {
		t.Fatal("expected error")
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Errorf("ctx cancel did not interrupt backoff sleep, took %v", time.Since(start))
	}
}

func TestDefaultClassifierRejectsContextErrors(t *testing.T) {
	if DefaultClassifier(context.Canceled, nil) {
		t.Error("context.Canceled should not be retried")
	}
	if DefaultClassifier(context.DeadlineExceeded, nil) {
		t.Error("context.DeadlineExceeded should not be retried")
	}
}

func TestBackoffGrowsExponentially(t *testing.T) {
	p := Policy{InitialBackoff: 100 * time.Millisecond, MaxBackoff: time.Second, Multiplier: 2.0, Jitter: 0}
	p.SetRand(rand.New(rand.NewSource(1)))
	b1 := p.nextBackoff(1, 0)
	b2 := p.nextBackoff(2, b1)
	b3 := p.nextBackoff(3, b2)
	if b1 != 100*time.Millisecond {
		t.Errorf("b1 = %v", b1)
	}
	if b2 != 200*time.Millisecond {
		t.Errorf("b2 = %v", b2)
	}
	if b3 != 400*time.Millisecond {
		t.Errorf("b3 = %v", b3)
	}
}

func TestBackoffCapsAtMax(t *testing.T) {
	p := Policy{InitialBackoff: 100 * time.Millisecond, MaxBackoff: 250 * time.Millisecond, Multiplier: 10, Jitter: 0}
	p.SetRand(rand.New(rand.NewSource(1)))
	b1 := p.nextBackoff(1, 0)
	b2 := p.nextBackoff(2, b1)
	if b2 > 250*time.Millisecond {
		t.Errorf("b2 = %v exceeds cap", b2)
	}
}

func TestBackoffApppliesJitter(t *testing.T) {
	p := Policy{InitialBackoff: 100 * time.Millisecond, MaxBackoff: time.Second, Multiplier: 2.0, Jitter: 0.5}
	p.SetRand(rand.New(rand.NewSource(42)))
	seen := make(map[time.Duration]int)
	for i := 0; i < 50; i++ {
		seen[p.nextBackoff(1, 0)]++
	}
	if len(seen) < 5 {
		t.Errorf("jitter should produce variation, only saw %d distinct values", len(seen))
	}
}
