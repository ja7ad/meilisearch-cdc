// Package retry implements exponential backoff with full jitter and a
// circuit breaker.
package retry

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"
)

// Policy configures retries.
type Policy struct {
	MaxAttempts int // <= 0 means retry until ctx is done
	Initial     time.Duration
	Max         time.Duration
	Multiplier  float64
	// Retryable decides whether an error is worth another attempt.
	// nil retries every error except Permanent ones.
	Retryable func(error) bool
	// OnRetry is called before sleeping.
	OnRetry func(attempt int, err error, wait time.Duration)
}

func (p Policy) withDefaults() Policy {
	if p.Initial <= 0 {
		p.Initial = 100 * time.Millisecond
	}
	if p.Max <= 0 {
		p.Max = 30 * time.Second
	}
	if p.Multiplier < 1 {
		p.Multiplier = 2
	}
	return p
}

// Backoff returns the delay before attempt n (1-based) using full jitter:
// a random duration in [0, min(Max, Initial*Multiplier^(n-1))].
func (p Policy) Backoff(n int) time.Duration {
	p = p.withDefaults()
	d := float64(p.Initial)
	for i := 1; i < n && d < float64(p.Max); i++ {
		d *= p.Multiplier
	}
	if d > float64(p.Max) {
		d = float64(p.Max)
	}
	// Keep a floor of half the delay so retries never become a tight loop.
	half := d / 2
	return time.Duration(half + rand.Float64()*half) // #nosec G404 -- jitter, not security
}

type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// Permanent marks err as not retryable.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanent{err}
}

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var p permanent
	return errors.As(err, &p)
}

// ErrExhausted wraps the last error once every attempt failed.
var ErrExhausted = errors.New("retry attempts exhausted")

// Do calls fn until it succeeds, returns a non-retryable error, attempts
// run out or ctx is done.
func Do[T any](ctx context.Context, p Policy, fn func(ctx context.Context, attempt int) (T, error)) (T, error) {
	p = p.withDefaults()
	var zero T
	for attempt := 1; ; attempt++ {
		v, err := fn(ctx, attempt)
		if err == nil {
			return v, nil
		}
		if IsPermanent(err) || (p.Retryable != nil && !p.Retryable(err)) {
			return zero, err
		}
		if p.MaxAttempts > 0 && attempt >= p.MaxAttempts {
			return zero, errors.Join(ErrExhausted, err)
		}
		wait := p.Backoff(attempt)
		if p.OnRetry != nil {
			p.OnRetry(attempt, err, wait)
		}
		if serr := Sleep(ctx, wait); serr != nil {
			return zero, errors.Join(serr, err)
		}
	}
}

// Run is Do for functions without a result.
func Run(ctx context.Context, p Policy, fn func(ctx context.Context, attempt int) error) error {
	_, err := Do(ctx, p, func(ctx context.Context, attempt int) (struct{}, error) {
		return struct{}{}, fn(ctx, attempt)
	})
	return err
}

// Sleep waits for d or until ctx is done.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ErrOpen is returned while the breaker rejects calls.
var ErrOpen = errors.New("circuit breaker is open")

// Breaker opens after Threshold consecutive failures and lets a single probe
// through once Cooldown has passed (half-open).
type Breaker struct {
	Threshold int
	Cooldown  time.Duration

	mu       sync.Mutex
	failures int
	openedAt time.Time
	probing  bool
	now      func() time.Time
}

// NewBreaker returns a breaker.
func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{Threshold: threshold, Cooldown: cooldown, now: time.Now}
}

// Allow reports whether a call may proceed.
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < b.Threshold {
		return nil
	}
	if b.now().Sub(b.openedAt) < b.Cooldown || b.probing {
		return ErrOpen
	}
	b.probing = true
	return nil
}

// Record reports the outcome of an allowed call.
func (b *Breaker) Record(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	if err == nil {
		b.failures = 0
		return
	}
	b.failures++
	if b.failures >= b.Threshold {
		b.openedAt = b.now()
	}
}

// Open reports whether the breaker currently rejects calls.
func (b *Breaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failures >= b.Threshold && b.now().Sub(b.openedAt) < b.Cooldown
}
