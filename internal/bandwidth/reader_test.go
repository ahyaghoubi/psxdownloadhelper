package bandwidth

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

func TestBucketUnlimitedSkipsThrottle(t *testing.T) {
	b := NewBucket(0, 0)
	if err := b.Take(context.Background(), 1<<30); err != nil {
		t.Errorf("unlimited Take returned err: %v", err)
	}
}

func TestLimitedReaderEnforcesRate(t *testing.T) {
	// 10 KB/s with 1 KB burst. Reading 5 KB should take ~400ms.
	const total = 5 * 1024
	b := NewBucket(10*1024, 1024)
	data := bytes.Repeat([]byte("x"), total)
	r := NewLimitedReader(context.Background(), bytes.NewReader(data), b, 1024)

	start := time.Now()
	out, err := io.ReadAll(r)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != total {
		t.Fatalf("len = %d, want %d", len(out), total)
	}
	// We had 1 KB free in the burst, then needed 4 KB more at 10 KB/s ≈ 400ms.
	// Be generous in the lower bound to avoid flakiness.
	if elapsed < 300*time.Millisecond {
		t.Errorf("read finished too fast: %v (want ≥300ms)", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("read took too long: %v", elapsed)
	}
}

func TestLimitedReaderHonoursContext(t *testing.T) {
	// Big bucket (burst 4096) so the first chunk drains it; subsequent
	// reads must wait at 1 byte/sec and hit the context deadline.
	b := NewBucket(1, 4096)
	data := bytes.Repeat([]byte("x"), 16*1024)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	r := NewLimitedReader(ctx, bytes.NewReader(data), b, 4096)
	buf := make([]byte, 4096)
	// First read consumes the entire burst.
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("first ReadFull: %v", err)
	}
	// Second read must block on Take until ctx fires.
	_, err := r.Read(buf)
	if err == nil {
		t.Fatal("expected context error on second read")
	}
}

func TestLimitedReaderCapsReadSize(t *testing.T) {
	b := NewBucket(0, 0) // unlimited so this only tests the size cap
	data := bytes.Repeat([]byte("x"), 1024)
	r := NewLimitedReader(context.Background(), bytes.NewReader(data), b, 128)
	buf := make([]byte, 1024)
	n, err := r.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if n > 128 {
		t.Errorf("read returned %d bytes, expected ≤128", n)
	}
}
