package lifecycle

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunDrainsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var done atomic.Int32

	svc := func(ctx context.Context) error {
		<-ctx.Done()
		done.Add(1)
		return ctx.Err()
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	if err := Run(ctx, Options{Timeout: time.Second}, svc, svc); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if done.Load() != 2 {
		t.Fatalf("expected 2 services drained, got %d", done.Load())
	}
}

func TestRunReturnsFirstError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	boom := errors.New("boom")
	err := Run(ctx, Options{Timeout: 50 * time.Millisecond},
		func(context.Context) error { return boom },
		func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	)
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want boom", err)
	}
}

func TestRunTimesOutDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Run(ctx, Options{Timeout: 50 * time.Millisecond},
		func(ctx context.Context) error {
			<-time.After(200 * time.Millisecond)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestGoService(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var stopped atomic.Bool
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := Run(ctx, Options{Timeout: time.Second}, GoService(func(c context.Context) {
		<-c.Done()
		stopped.Store(true)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !stopped.Load() {
		t.Fatal("service did not stop")
	}
}
