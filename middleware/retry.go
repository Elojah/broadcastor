package middleware

import (
	"context"
	"math"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/subscriber"
)

// RetryPolicy sets how many times Retry calls the handler it wraps for one message, and how long it waits between two
// calls. Its zero value never retries.
type RetryPolicy struct {
	// Attempts is the most times the handler is called for one message, the first call included. 1 or less never
	// retries.
	Attempts int

	// Delay is how long Retry waits before the first retry. 0 retries right away.
	Delay time.Duration

	// Multiplier makes each wait after the first one the previous wait times Multiplier. 1 or less keeps every wait at
	// Delay.
	Multiplier float64

	// MaxDelay caps every wait, Delay included. 0 means no cap.
	MaxDelay time.Duration

	// Jitter draws each wait at random between the wait times 1 - Jitter and the wait itself, so that subscribers
	// failing at the same time do not all retry at the same time. It is clamped to [0, 1]: 0 means no jitter, and a wait
	// is never longer than without it.
	Jitter float64

	// IsRetryable reports whether an error the handler returned is worth another call. Nil retries every error.
	IsRetryable func(err error) bool
}

// Retry calls the handler it wraps again when it returns an error, as policy sets, until a call succeeds. It returns
// the error of the last call as is: once policy.Attempts calls were made, once policy.IsRetryable rejects the error, or
// once ctx is done. The first call is always made, whatever ctx.
//
// Retry waits in the subscriber's goroutine, so each wait holds the subscriber up like a slow handle does, and with it
// any Broadcast waiting for the subscriber to take a message. That is fine for a few quick attempts, not for retrying
// minutes later. Unsubscribe does not end a wait, but the ctx passed to Subscribe being done does, which
// subscriber.WithAutoUnsubscribe ties to the subscription.
//
// A panic is not an error, so Retry does not retry it. Given after Recover, as recommended, it lets the panic through
// to Recover right away. Given before Recover, it retries the *subscriber.PanicError like any other error.
func Retry[T any](policy RetryPolicy) subscriber.Middleware[T] {
	return func(next subscriber.Handler[T]) subscriber.Handler[T] {
		return func(ctx context.Context, id uuid.UUID, msg T) error {
			delay := policy.bound(policy.Delay)
			for attempt := 1; ; attempt++ {
				err := next(ctx, id, msg)
				if err == nil || attempt >= policy.Attempts || (policy.IsRetryable != nil && !policy.IsRetryable(err)) {
					return err
				}
				if !wait(ctx, policy.jitter(delay)) {
					return err
				}
				delay = policy.bound(policy.grow(delay))
			}
		}
	}
}

// grow returns the wait after a wait of d, before it is capped: d times p.Multiplier, if that is more than 1.
func (p RetryPolicy) grow(d time.Duration) time.Duration {
	if p.Multiplier <= 1 {
		return d
	}
	// Compared as a float, since converting one that does not fit to a time.Duration gives an unspecified value.
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

// jitter returns a wait drawn at random between d times 1 - p.Jitter and d.
func (p RetryPolicy) jitter(d time.Duration) time.Duration {
	if p.Jitter <= 0 {
		return d
	}

	return d - time.Duration(rand.Float64()*min(p.Jitter, 1)*float64(d)) //nolint:gosec // A wait needs no cryptographic randomness.
}

// wait waits for d, and reports whether it did before ctx was done.
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
