package capture

import (
	"sync"
	"sync/atomic"
)

// Bus broadcasts capture events to all subscribers. Implementations must be
// safe for concurrent Publish and Subscribe calls.
//
// Back-pressure policy: publishers never block. When a subscriber's buffer is
// full, the event is dropped for that subscriber only (counted via Dropped).
// Plan.md §6.3 requires the proxy never to stall waiting on a slow consumer.
type Bus interface {
	Publish(Event)
	Subscribe() (<-chan Event, func())
	Dropped() uint64
}

// NewBus returns an in-memory Bus where each subscriber receives a channel
// buffered up to bufferSize events. A bufferSize of 0 means unbuffered.
func NewBus(bufferSize int) Bus {
	if bufferSize < 0 {
		bufferSize = 0
	}
	return &memBus{
		subscribers: make(map[uint64]*subscriber),
		bufferSize:  bufferSize,
	}
}

type subscriber struct {
	ch     chan Event
	closed chan struct{}
}

type memBus struct {
	mu          sync.RWMutex
	subscribers map[uint64]*subscriber
	nextID      uint64
	bufferSize  int
	dropped     atomic.Uint64
}

func (b *memBus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subscribers {
		select {
		case <-s.closed:
			// Subscriber is gone; skip.
		case s.ch <- e:
			// Delivered.
		default:
			// Buffer full; drop for this subscriber.
			b.dropped.Add(1)
		}
	}
}

func (b *memBus) Subscribe() (<-chan Event, func()) {
	s := &subscriber{
		ch:     make(chan Event, b.bufferSize),
		closed: make(chan struct{}),
	}
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subscribers[id] = s
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			close(s.closed)
			b.mu.Lock()
			delete(b.subscribers, id)
			b.mu.Unlock()
		})
	}
	return s.ch, unsubscribe
}

func (b *memBus) Dropped() uint64 {
	return b.dropped.Load()
}
