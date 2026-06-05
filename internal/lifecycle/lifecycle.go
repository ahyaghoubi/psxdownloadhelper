// Package lifecycle coordinates graceful shutdown of long-running services.
package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// DefaultTimeout is how long Run waits for services to exit after ctx is canceled.
const DefaultTimeout = 15 * time.Second

// Options configures Run.
type Options struct {
	Logger  *slog.Logger
	Timeout time.Duration
	// Cancel is invoked when a service returns a non-cancel error so siblings stop.
	Cancel context.CancelFunc
}

// Run starts each service in its own goroutine. It blocks until ctx is canceled
// (typically via signal.NotifyContext) or any service returns a non-cancel error,
// then waits for all remaining services up to Timeout.
func Run(ctx context.Context, opts Options, services ...func(context.Context) error) error {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if len(services) == 0 {
		return nil
	}

	errCh := make(chan error, len(services))
	for _, svc := range services {
		go func(fn func(context.Context) error) {
			errCh <- fn(ctx)
		}(svc)
	}

	var (
		firstErr  error
		remaining = len(services)
	)

	waitOne := func() bool {
		select {
		case err := <-errCh:
			remaining--
			if err != nil && !errors.Is(err, context.Canceled) && firstErr == nil {
				firstErr = err
			}
			return true
		case <-ctx.Done():
			return false
		}
	}

	// Block until ctx is canceled or a service fails.
	for remaining > 0 {
		if waitOne() {
			if firstErr != nil {
				if opts.Cancel != nil {
					opts.Cancel()
				}
				break
			}
			continue
		}
		opts.Logger.Info("shutting down gracefully")
		break
	}

	// Drain remaining services.
	deadline := time.After(opts.Timeout)
	timedOut := false
	for remaining > 0 {
		select {
		case err := <-errCh:
			remaining--
			if err != nil && !errors.Is(err, context.Canceled) && firstErr == nil {
				firstErr = err
			}
		case <-deadline:
			timedOut = true
			remaining = 0
		}
	}

	if timedOut {
		opts.Logger.Warn("shutdown timed out; some work may have been interrupted", "timeout", opts.Timeout)
	}
	return firstErr
}

// GoService wraps a void function as a lifecycle service.
func GoService(fn func(context.Context)) func(context.Context) error {
	return func(ctx context.Context) error {
		fn(ctx)
		return nil
	}
}
