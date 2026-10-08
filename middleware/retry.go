package middleware

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/subscriber"
)

// RetryPolicy sets how Retry retries. Its zero value never retries.
type RetryPolicy struct {
	// Attempts is the maximum number of calls, the first included.
	Attempts int

	// Delay is the wait before the first retry.
	Delay time.Duration

	// Multiplier scales each later wait. 1 or less keeps it constant.
	Multiplier float64

	// MaxDelay caps every wait. 0 means no cap.
	MaxDelay time.Duration

	// Jitter shortens each wait by a random fraction up to Jitter, clamped to [0, 1], so that subscribers failing
	// together retry apart.
	Jitter float64

	// IsRetryable reports whether an error is worth retrying. nil retries every error.
	IsRetryable func(err error) bool
}

// Retry calls the handler with policy.Do, so again after an error, but never retries subscriber.ErrClosed, which an
// ended SubscribeSeq loop returns.
//
// It waits in the subscriber's goroutine, holding up the subscriber and any Broadcast waiting on it. Unsubscribe does
// not end a wait: ctx does. A panic passes through, but an inner Recover's *subscriber.PanicError is retried.
func Retry[T any](policy RetryPolicy) subscriber.Middleware[T] {
	isRetryable := policy.IsRetryable
	policy.IsRetryable = func(err error) bool {
		return !errors.Is(err, subscriber.ErrClosed) && (isRetryable == nil || isRetryable(err))
	}

	return func(next subscriber.Handler[T]) subscriber.Handler[T] {
		return func(ctx context.Context, id uuid.UUID, msg T) error {
			return policy.Do(ctx, func(ctx context.Context) error { return next(ctx, id, msg) })
		}
	}
}

// Do calls f with ctx until it succeeds, as p sets, and returns its last error: a dial, say, with the same backoff as
// Retry. It always makes the first call, and stops once ctx is done, even mid-wait.
func (p RetryPolicy) Do(ctx context.Context, f func(ctx context.Context) error) error {
	delay := p.bound(p.Delay)
	for attempt := 1; ; attempt++ {
		err := f(ctx)
		if err == nil || attempt >= p.Attempts || (p.IsRetryable != nil && !p.IsRetryable(err)) {
			return err
		}
		if !wait(ctx, p.jitter(delay)) {
			return err
		}
		delay = p.bound(p.grow(delay))
	}
}

// grow returns the uncapped wait after d.
func (p RetryPolicy) grow(d time.Duration) time.Duration {
	if p.Multiplier <= 1 {
		return d
	}
	// Compared as a float: converting an out-of-range float to a Duration is unspecified.
	if next := float64(d) * p.Multiplier; next < math.MaxInt64 {
		return time.Duration(next)
	}

	return math.MaxInt64
}

// bound caps d at p.MaxDelay, unless that is 0.
func (p RetryPolicy) bound(d time.Duration) time.Duration {
	if p.MaxDelay > 0 {
		return min(d, p.MaxDelay)
	}

	return d
}

// jitter returns a random wait between d times 1 - p.Jitter and d.
func (p RetryPolicy) jitter(d time.Duration) time.Duration {
	if p.Jitter <= 0 {
		return d
	}

	return d - time.Duration(rand.Float64()*min(p.Jitter, 1)*float64(d)) //nolint:gosec // A wait needs no cryptographic randomness.
}

// wait waits for d, and reports whether ctx is still not done.
func wait(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
