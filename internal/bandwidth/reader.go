// Package bandwidth implements a simple token-bucket rate limiter for
// io.Reader. The proxy uses it to cap the throughput of the upstream
// forward path, so psxdh's forwards don't starve a parallel FDM
// download on the same WAN link.
//
// The algorithm is the classic token bucket:
//   - The bucket starts full (capacity = BurstBytes).
//   - Every Read consumes tokens equal to the number of bytes actually
//     read. If the bucket is empty, Read sleeps until enough tokens
//     accumulate at the configured RatePerSec, then reads.
//   - Tokens refill linearly between Reads, capped at BurstBytes.
//
// The implementation is single-reader: each NewLimitedReader call returns
// a reader with its own bucket. To share a bucket across multiple readers
// (e.g. a global cap across concurrent forwards), use NewSharedBucket.
package bandwidth

import (
	"context"
	"io"
	"sync"
	"time"
)

// Bucket is a token bucket. It's safe for concurrent use; multiple
// LimitedReader instances can share one.
type Bucket struct {
	rate    int64 // tokens per second
	burst   int64 // max tokens
	mu      sync.Mutex
	tokens  float64
	updated time.Time
	now     func() time.Time
}

// NewBucket creates a token bucket with the given rate (bytes/sec) and
// burst capacity (bytes). A non-positive rate means "unlimited" and Take
// is a no-op (the reader behaves as if no limiter were attached).
func NewBucket(ratePerSec, burst int64) *Bucket {
	if burst <= 0 {
		burst = ratePerSec
	}
	if burst <= 0 {
		burst = 1
	}
	return &Bucket{
		rate:    ratePerSec,
		burst:   burst,
		tokens:  float64(burst),
		updated: time.Now(),
		now:     time.Now,
	}
}

// Take blocks until n tokens are available, or ctx is canceled. It
// returns nil on success, or ctx.Err() on cancellation. When rate <= 0
// the call returns immediately.
//
// Requests larger than the bucket's burst are chunked internally — the
// bucket can never accumulate more tokens than its burst, so asking for
// 100 KB from a 32 KB bucket consumes 4 sequential waits of 32 KB each.
func (b *Bucket) Take(ctx context.Context, n int64) error {
	if b.rate <= 0 || n <= 0 {
		return nil
	}
	for n > 0 {
		chunk := n
		if chunk > b.burst {
			chunk = b.burst
		}
		if err := b.takeAtMostBurst(ctx, chunk); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

// takeAtMostBurst waits for up to b.burst tokens. Caller must guarantee
// n <= b.burst — otherwise the wait math doesn't converge.
func (b *Bucket) takeAtMostBurst(ctx context.Context, n int64) error {
	for {
		b.mu.Lock()
		now := b.now()
		elapsed := now.Sub(b.updated).Seconds()
		b.updated = now
		b.tokens += elapsed * float64(b.rate)
		if b.tokens > float64(b.burst) {
			b.tokens = float64(b.burst)
		}
		need := float64(n) - b.tokens
		if need <= 0 {
			b.tokens -= float64(n)
			b.mu.Unlock()
			return nil
		}
		wait := time.Duration(need / float64(b.rate) * float64(time.Second))
		b.mu.Unlock()

		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// Burst returns the bucket's burst size in bytes.
func (b *Bucket) Burst() int64 { return b.burst }

// Rate returns the configured rate in bytes/second.
func (b *Bucket) Rate() int64 { return b.rate }

// LimitedReader wraps r so that Reads are throttled by bucket. ReadSize
// caps the chunk size requested per Read so very large p slices don't
// block for many seconds waiting on tokens.
type LimitedReader struct {
	r        io.Reader
	bucket   *Bucket
	ctx      context.Context
	readSize int
}

// NewLimitedReader returns a reader throttled by bucket. ctx cancels
// pending Take waits — pass r.Context() when wrapping an http.Request
// body. readSize <= 0 defaults to 32 KiB.
func NewLimitedReader(ctx context.Context, r io.Reader, bucket *Bucket, readSize int) *LimitedReader {
	if readSize <= 0 {
		readSize = 32 * 1024
	}
	return &LimitedReader{r: r, bucket: bucket, ctx: ctx, readSize: readSize}
}

// Read implements io.Reader.
func (l *LimitedReader) Read(p []byte) (int, error) {
	// Cap the chunk to the smaller of (configured readSize, bucket burst)
	// so a single Read never blocks longer than one burst-worth of time
	// and Take never has to chunk internally.
	maxChunk := l.readSize
	if l.bucket.rate > 0 && int(l.bucket.burst) > 0 && int(l.bucket.burst) < maxChunk {
		maxChunk = int(l.bucket.burst)
	}
	if len(p) > maxChunk {
		p = p[:maxChunk]
	}
	if err := l.bucket.Take(l.ctx, int64(len(p))); err != nil {
		return 0, err
	}
	n, err := l.r.Read(p)
	// We pre-paid for len(p) tokens but only used n. Refund the
	// difference so we don't over-throttle on short reads.
	if int64(len(p)-n) > 0 && l.bucket.rate > 0 {
		l.bucket.mu.Lock()
		l.bucket.tokens += float64(len(p) - n)
		if l.bucket.tokens > float64(l.bucket.burst) {
			l.bucket.tokens = float64(l.bucket.burst)
		}
		l.bucket.mu.Unlock()
	}
	return n, err
}
