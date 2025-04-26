// Package events provides typed in-process publish/subscribe and ring buffers.
package events

import (
	"sync"
	"sync/atomic"
)

// Bus fans out values of type T to subscribers. Slow subscribers drop
// messages instead of blocking publishers.
type Bus[T any] struct {
	mu   sync.RWMutex
	subs map[*Subscription[T]]struct{}
}

// Subscription receives values on C until Close.
type Subscription[T any] struct {
	C       <-chan T
	c       chan T
	bus     *Bus[T]
	dropped atomic.Uint64
	once    sync.Once
}

// NewBus returns an empty bus.
func NewBus[T any]() *Bus[T] {
	return &Bus[T]{subs: map[*Subscription[T]]struct{}{}}
}

// Subscribe registers a subscriber with the given buffer size.
func (b *Bus[T]) Subscribe(buffer int) *Subscription[T] {
	c := make(chan T, buffer)
	s := &Subscription[T]{C: c, c: c, bus: b}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Publish delivers v to every subscriber that has room.
func (b *Bus[T]) Publish(v T) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.c <- v:
		default:
			s.dropped.Add(1)
		}
	}
}

// Len returns the number of subscribers.
func (b *Bus[T]) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Dropped returns how many values were dropped because C was full.
func (s *Subscription[T]) Dropped() uint64 { return s.dropped.Load() }

// Close unsubscribes and closes C.
func (s *Subscription[T]) Close() {
	s.once.Do(func() {
		s.bus.mu.Lock()
		delete(s.bus.subs, s)
		s.bus.mu.Unlock()
		close(s.c)
	})
}

// Ring is a fixed capacity, concurrency safe circular buffer that keeps the
// most recent values.
type Ring[T any] struct {
	mu   sync.RWMutex
	buf  []T
	next int
	full bool
}

// NewRing returns a ring holding up to size values.
func NewRing[T any](size int) *Ring[T] {
	if size < 1 {
		size = 1
	}
	return &Ring[T]{buf: make([]T, size)}
}

// Push appends v, evicting the oldest value when full.
func (r *Ring[T]) Push(v T) {
	r.mu.Lock()
	r.buf[r.next] = v
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

// Values returns the values oldest first.
func (r *Ring[T]) Values() []T {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.full {
		return append([]T(nil), r.buf[:r.next]...)
	}
	out := make([]T, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	return append(out, r.buf[:r.next]...)
}

// Last returns up to n most recent values, oldest first.
func (r *Ring[T]) Last(n int) []T {
	v := r.Values()
	if n > 0 && len(v) > n {
		v = v[len(v)-n:]
	}
	return v
}

// Clear drops every value and returns how many there were.
func (r *Ring[T]) Clear() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.next
	if r.full {
		n = len(r.buf)
	}
	clear(r.buf)
	r.next, r.full = 0, false
	return n
}
