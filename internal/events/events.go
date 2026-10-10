// Package events is ThothDock's event bus: a bounded in-memory ring for
// replays and push delivery to live subscribers (docs/nextgen/adr/ADR-0003).
// Publishing never blocks and nothing runs while nobody subscribes.
package events

import (
	"sync"
	"time"
)

// Event is one engine event, in dockerd's vocabulary.
type Event struct {
	Type   string // container, image, network, volume
	Action string // create, start, die, stop, kill, restart, destroy, ...
	ID     string
	Attrs  map[string]string
	Time   time.Time
}

const (
	// RingSize is how many past events a subscriber can replay.
	RingSize = 1024
	// SubscriberBuffer is how far a subscriber may fall behind before it
	// is disconnected.
	SubscriberBuffer = 256
)

// Bus fans events out to subscribers.
type Bus struct {
	mu   sync.Mutex
	ring [RingSize]Event
	n    uint64 // events published so far
	subs map[*Subscription]struct{}
	now  func() time.Time
}

// Subscription is a live stream of events. C is closed when the bus
// disconnects a subscriber that fell behind, or after Close.
type Subscription struct {
	C       <-chan Event
	c       chan Event
	bus     *Bus
	dropped bool
}

// New returns an empty bus.
func New() *Bus {
	return &Bus{subs: map[*Subscription]struct{}{}, now: time.Now}
}

// Publish records an event and delivers it to every subscriber.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e.Time.IsZero() {
		e.Time = b.now()
	}
	// Two events in the same nanosecond keep their publication order.
	if b.n > 0 {
		if last := b.ring[(b.n-1)%RingSize].Time; !e.Time.After(last) {
			e.Time = last.Add(time.Nanosecond)
		}
	}
	b.ring[b.n%RingSize] = e
	b.n++
	for s := range b.subs {
		select {
		case s.c <- e:
		default:
			s.dropped = true
			delete(b.subs, s)
			close(s.c)
		}
	}
}

// Subscribe returns the retained events newer than since (all retained
// events when since is zero) and a subscription for everything published
// afterwards, with nothing lost or repeated between the two.
func (b *Bus) Subscribe(since time.Time) ([]Event, *Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var past []Event
	first := uint64(0)
	if b.n > RingSize {
		first = b.n - RingSize
	}
	for i := first; i < b.n; i++ {
		if e := b.ring[i%RingSize]; since.IsZero() || e.Time.After(since) {
			past = append(past, e)
		}
	}
	c := make(chan Event, SubscriberBuffer)
	s := &Subscription{C: c, c: c, bus: b}
	b.subs[s] = struct{}{}
	return past, s
}

// Close ends the subscription.
func (s *Subscription) Close() {
	b := s.bus
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[s]; ok {
		delete(b.subs, s)
		close(s.c)
	}
}

// Dropped reports whether the bus disconnected this subscriber because it
// fell behind. Valid once C is closed.
func (s *Subscription) Dropped() bool {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	return s.dropped
}

// Subscribers is the number of live subscriptions.
func (b *Bus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
