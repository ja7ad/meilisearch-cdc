package auth

import (
	"sync"
	"time"
)

// Limiter counts failures per key in a fixed window and blocks a key once
// it reaches the limit, to slow down password guessing.
type Limiter struct {
	Max    int
	Window time.Duration

	mu   sync.Mutex
	hits map[string]*window
	now  func() time.Time
}

type window struct {
	start time.Time
	n     int
}

// NewLimiter returns a limiter allowing max failures per window.
func NewLimiter(max int, w time.Duration) *Limiter {
	return &Limiter{Max: max, Window: w, hits: map[string]*window{}, now: time.Now}
}

// Blocked reports whether key is blocked and for how long.
func (l *Limiter) Blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.hits[key]
	if !ok {
		return false, 0
	}
	elapsed := l.now().Sub(w.start)
	if elapsed >= l.Window {
		delete(l.hits, key)
		return false, 0
	}
	if w.n >= l.Max {
		return true, l.Window - elapsed
	}
	return false, 0
}

// Fail records a failure for key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) >= l.Window {
		l.hits[key] = &window{start: now, n: 1}
	} else {
		w.n++
	}
	// Opportunistic cleanup keeps memory bounded.
	if len(l.hits) > 10000 {
		for k, v := range l.hits {
			if now.Sub(v.start) >= l.Window {
				delete(l.hits, k)
			}
		}
	}
}

// Reset clears key after a success.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}
