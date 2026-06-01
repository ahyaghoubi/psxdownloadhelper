// Package retry implements the backoff policy used by the proxy's
// upstream forward path. The invariant the package enforces is the only
// one that matters for psxdh: retries are valid only BEFORE any response
// bytes have reached the client. Once we've started streaming the body,
// a mid-stream upstream failure has to bubble up so the console can
// re-issue with a Range header. See docs/architecture.md.
//
// Calling Do with an Op that has already written to the wire is the
// caller's responsibility; the package cannot detect that itself.
package retry

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Policy describes how aggressively to retry transient failures.
//
// The zero Policy is "no retry" — MaxAttempts of 0 or 1 means a single
// attempt with no backoff.
type Policy struct {
	// MaxAttempts is the total number of attempts (initial + retries).
	// A MaxAttempts of 1 disables retry. Values <= 0 are treated as 1.
	MaxAttempts int
	// InitialBackoff is the wait before the second attempt. Subsequent
	// waits multiply by Multiplier, capped at MaxBackoff.
	InitialBackoff time.Duration
	// MaxBackoff is the cap on any single sleep.
	MaxBackoff time.Duration
	// Multiplier is applied to the backoff after each attempt.
	// Values <= 1.0 are treated as 1.0 (constant backoff).
	Multiplier float64
	// Jitter is the [0,1] fraction of the computed backoff to randomise.
	// 0 disables jitter; 0.2 (the default) introduces ±20% noise.
	Jitter float64
	// rand is overridable for deterministic tests.
	rand *rand.Rand
	mu   sync.Mutex
}

// SetRand overrides the random source for jitter. Used by tests.
func (p *Policy) SetRand(r *rand.Rand) {
	p.mu.Lock()
	p.rand = r
	p.mu.Unlock()
}

// Op is the retryable operation. Returning a nil err is success and stops
// the loop. The returned response can be nil on failure.
type Op func(attempt int) (*http.Response, error)

// Classifier decides whether an error / response pair is worth retrying.
// Custom classifiers can layer on top of DefaultClassifier; e.g. the
// proxy adds "do not retry if response body has been written yet".
type Classifier func(err error, resp *http.Response) bool

// DefaultClassifier returns true for transient failures: DNS errors,
// connection refused / reset / timeout, TLS handshake failures, and the
// 5xx server-error class. 4xx responses and context cancellation are
// NOT retried — the caller's intent has either been refused for a
// non-network reason or the user wants out.
func DefaultClassifier(err error, resp *http.Response) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		var ne net.Error
		if errors.As(err, &ne) {
			return true
		}
		// String-sniffing is unfortunate but the stdlib doesn't expose
		// every transient class as a typed error.
		s := err.Error()
		for _, sig := range transientSubstrings {
			if strings.Contains(s, sig) {
				return true
			}
		}
		return false
	}
	if resp != nil {
		switch resp.StatusCode {
		case http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout,
			http.StatusInternalServerError:
			return true
		}
	}
	return false
}

var transientSubstrings = []string{
	"connection reset",
	"connection refused",
	"broken pipe",
	"no such host",
	"tls handshake",
	"unexpected EOF",
	"i/o timeout",
	"network is unreachable",
	"no route to host",
}

// Do executes op up to p.MaxAttempts times, sleeping between attempts.
// It returns the final (resp, err) pair. The caller closes resp.Body.
func (p *Policy) Do(ctx context.Context, classify Classifier, op Op) (*http.Response, error) {
	if classify == nil {
		classify = DefaultClassifier
	}
	attempts := p.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var (
		resp     *http.Response
		err      error
		lastWait time.Duration
	)
	for i := 1; i <= attempts; i++ {
		resp, err = op(i)
		// Success path: no error and response is not retry-worthy.
		if err == nil && resp != nil && !classify(nil, resp) {
			return resp, nil
		}
		// Non-retriable error: surface immediately.
		if err != nil && !classify(err, nil) {
			return resp, err
		}
		// Last attempt: return whatever we have.
		if i == attempts {
			return resp, err
		}
		// Going to retry — drain and close any response body so the
		// underlying connection can be reused / cleaned up.
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		lastWait = p.nextBackoff(i, lastWait)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lastWait):
		}
	}
	return resp, err
}

// nextBackoff returns the wait before attempt+1. lastWait is the previous
// sleep duration; the first call should pass 0.
func (p *Policy) nextBackoff(attempt int, lastWait time.Duration) time.Duration {
	initial := p.InitialBackoff
	if initial <= 0 {
		initial = 200 * time.Millisecond
	}
	maxBackoff := p.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 5 * time.Second
	}
	mult := p.Multiplier
	if mult <= 1.0 {
		mult = 2.0
	}
	var base time.Duration
	if lastWait == 0 {
		base = initial
	} else {
		base = time.Duration(float64(lastWait) * mult)
	}
	if base > maxBackoff {
		base = maxBackoff
	}
	jitter := p.Jitter
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	if jitter > 0 {
		p.mu.Lock()
		if p.rand == nil {
			p.rand = rand.New(rand.NewSource(time.Now().UnixNano()))
		}
		noise := (p.rand.Float64()*2 - 1) * jitter // in [-jitter, +jitter]
		p.mu.Unlock()
		scaled := float64(base) * (1 + noise)
		if scaled < 0 {
			scaled = 0
		}
		base = time.Duration(scaled)
	}
	return base
}
