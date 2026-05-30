package capture

import (
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
)

func newEvent(path string, kind match.Kind) Event {
	u, _ := url.Parse("http://example.com" + path)
	return Event{
		URL:    u,
		Method: "GET",
		Kind:   kind,
		Time:   time.Now(),
	}
}

func TestPublishToMultipleSubscribers(t *testing.T) {
	b := NewBus(8)
	chA, unA := b.Subscribe()
	defer unA()
	chB, unB := b.Subscribe()
	defer unB()

	want := newEvent("/x", match.KindPKGApp)
	b.Publish(want)

	for name, ch := range map[string]<-chan Event{"A": chA, "B": chB} {
		select {
		case got := <-ch:
			if got.Kind != want.Kind {
				t.Errorf("%s: kind = %q, want %q", name, got.Kind, want.Kind)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not receive event", name)
		}
	}
}

func TestSlowSubscriberDoesNotBlockOthers(t *testing.T) {
	b := NewBus(1)
	// Subscriber A buffers 1; we never drain it. It will fill and drop the rest.
	chA, unA := b.Subscribe()
	defer unA()
	chB, unB := b.Subscribe()
	defer unB()

	// Drain B in the background so publishers can keep up.
	var receivedB atomic.Int64
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		timeout := time.After(2 * time.Second)
		for {
			select {
			case <-chB:
				receivedB.Add(1)
				if receivedB.Load() >= 100 {
					return
				}
			case <-timeout:
				return
			}
		}
	}()

	start := time.Now()
	for i := 0; i < 100; i++ {
		b.Publish(newEvent("/y", match.KindPKGApp))
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Errorf("publish should not block on slow subscriber; took %v", elapsed)
	}

	<-doneB
	if got := receivedB.Load(); got < 1 {
		t.Errorf("subscriber B should receive events; got %d", got)
	}
	// A's buffer should be saturated, and drops should be recorded.
	_ = chA
	if b.Dropped() == 0 {
		t.Errorf("expected non-zero drops for the slow subscriber; got 0")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	b := NewBus(4)
	ch, un := b.Subscribe()

	b.Publish(newEvent("/before", match.KindPKGApp))
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected first event")
	}

	un()
	un() // idempotent

	// Publish after unsubscribe must not panic and must not deliver.
	b.Publish(newEvent("/after", match.KindPKGApp))
	select {
	case ev := <-ch:
		// A residual buffered event from before unsubscribe would be ok,
		// but our "before" event was already drained. A new delivery is a bug.
		t.Errorf("received unexpected event after unsubscribe: %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// Expected: no delivery.
	}
}

func TestPublishWithNoSubscribers(t *testing.T) {
	b := NewBus(4)
	// Must not panic with zero subscribers.
	for i := 0; i < 1000; i++ {
		b.Publish(newEvent("/z", match.KindPKGApp))
	}
}

func TestConcurrentPublishersAndSubscribers(t *testing.T) {
	b := NewBus(1024)

	const subs = 4
	const eventsPerPublisher = 1000
	const publishers = 4

	var wg sync.WaitGroup
	var receivedTotal atomic.Int64

	for i := 0; i < subs; i++ {
		ch, un := b.Subscribe()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer un()
			deadline := time.After(5 * time.Second)
			for {
				select {
				case <-ch:
					receivedTotal.Add(1)
				case <-deadline:
					return
				}
			}
		}()
	}

	// Give subscribers a moment to register.
	time.Sleep(10 * time.Millisecond)

	var pubWG sync.WaitGroup
	for p := 0; p < publishers; p++ {
		pubWG.Add(1)
		go func() {
			defer pubWG.Done()
			for i := 0; i < eventsPerPublisher; i++ {
				b.Publish(newEvent("/concurrent", match.KindPKGApp))
			}
		}()
	}
	pubWG.Wait()

	// Allow subscribers to drain.
	time.Sleep(100 * time.Millisecond)

	// Each event should reach each subscriber, modulo any drops if the buffer
	// proved too small. With buffer=1024 and 4000 events spread over 4 subs,
	// we expect close to 16000 deliveries.
	got := receivedTotal.Load()
	if got < int64(subs*eventsPerPublisher*publishers/2) {
		t.Errorf("delivered events = %d; expected at least half of %d", got, subs*eventsPerPublisher*publishers)
	}

	wg.Wait()
}

func TestNegativeBufferSizeDoesNotPanic(t *testing.T) {
	b := NewBus(-5)
	if b == nil {
		t.Fatal("NewBus returned nil")
	}
	// Negative is sanitised to zero (unbuffered). Subscribe + Publish must
	// not panic. With an unbuffered channel and non-blocking publish, the
	// delivery is best-effort by design, so we don't assert receipt.
	_, un := b.Subscribe()
	defer un()
	b.Publish(newEvent("/zero-buf", match.KindPKGApp))
}
