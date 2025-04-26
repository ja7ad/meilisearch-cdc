package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fast = Policy{Initial: time.Millisecond, Max: 4 * time.Millisecond}

func TestDoRetriesUntilSuccess(t *testing.T) {
	calls := 0
	retried := 0
	p := fast
	p.OnRetry = func(int, error, time.Duration) { retried++ }
	v, err := Do(context.Background(), p, func(_ context.Context, attempt int) (string, error) {
		calls++
		if attempt < 3 {
			return "", errors.New("transient")
		}
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", v)
	assert.Equal(t, 3, calls)
	assert.Equal(t, 2, retried)
}

func TestDoStopsOnPermanent(t *testing.T) {
	calls := 0
	boom := errors.New("bad credentials")
	_, err := Do(context.Background(), fast, func(context.Context, int) (int, error) {
		calls++
		return 0, Permanent(boom)
	})
	assert.ErrorIs(t, err, boom)
	assert.True(t, IsPermanent(err))
	assert.Equal(t, 1, calls)
}

func TestDoHonoursRetryable(t *testing.T) {
	p := fast
	p.Retryable = func(err error) bool { return err.Error() != "nope" }
	calls := 0
	err := Run(context.Background(), p, func(context.Context, int) error { calls++; return errors.New("nope") })
	assert.Error(t, err)
	assert.Equal(t, 1, calls)
}

func TestDoExhausts(t *testing.T) {
	p := fast
	p.MaxAttempts = 4
	calls := 0
	err := Run(context.Background(), p, func(context.Context, int) error { calls++; return errors.New("down") })
	assert.ErrorIs(t, err, ErrExhausted)
	assert.Equal(t, 4, calls)
}

func TestDoCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := Policy{Initial: time.Hour, Max: time.Hour}
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	err := Run(ctx, p, func(context.Context, int) error { return errors.New("down") })
	assert.ErrorIs(t, err, context.Canceled)
}

func TestBackoffBounds(t *testing.T) {
	p := Policy{Initial: 100 * time.Millisecond, Max: time.Second}
	for n := 1; n < 20; n++ {
		d := p.Backoff(n)
		assert.GreaterOrEqual(t, d, 50*time.Millisecond)
		assert.LessOrEqual(t, d, time.Second)
	}
	assert.GreaterOrEqual(t, p.Backoff(10), 500*time.Millisecond, "late attempts reach the cap")
}

func TestBreaker(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewBreaker(2, time.Second)
	b.now = func() time.Time { return now }
	require.NoError(t, b.Allow())
	b.Record(errors.New("x"))
	require.NoError(t, b.Allow())
	b.Record(errors.New("x"))
	assert.ErrorIs(t, b.Allow(), ErrOpen)
	assert.True(t, b.Open())
	now = now.Add(2 * time.Second)
	require.NoError(t, b.Allow(), "half-open probe")
	assert.ErrorIs(t, b.Allow(), ErrOpen, "only one probe")
	b.Record(nil)
	require.NoError(t, b.Allow())
	assert.False(t, b.Open())
}
