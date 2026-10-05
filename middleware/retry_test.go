package middleware_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
)

// The tests that check how long Retry waits run in a synctest bubble, whose fake clock makes the time between two calls
// exactly the wait.

// Retry calls the handler again after each error, with the same ctx, subscriber and message, first after Delay and then
// after Delay times Multiplier, and returns nil once a call succeeds.
func TestRetry(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		ctx := context.WithValue(t.Context(), key{}, "subscribe")
		id := uuid.New()
		f := &flaky{errs: []error{attemptError(1), attemptError(2)}}
		handle := middleware.Retry[int](middleware.RetryPolicy{Attempts: 5, Delay: time.Second, Multiplier: 3})(
			func(ctx context.Context, gotID uuid.UUID, msg int) error {
				if v := ctx.Value(key{}); v != "subscribe" || gotID != id || msg != 7 {
					t.Errorf("handler got ctx value %v, subscriber %s and message %d, want %q, %s and 7", v, gotID, msg, "subscribe", id)
				}

				return f.handle(ctx, gotID, msg)
			})

		if err := handle(ctx, id, 7); err != nil {
			t.Errorf("Retry returned %v, want nil once a call succeeds", err)
		}
		if got, want := f.gaps(), []time.Duration{time.Second, 3 * time.Second}; !slices.Equal(got, want) {
			t.Errorf("Retry waited %v between calls, want %v", got, want)
		}
	})
}

// With a handler that always fails, Retry makes Attempts calls, waits as Delay, Multiplier and MaxDelay say, and
// returns the error of the last call as is.
func TestRetry_Backoff(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		policy middleware.RetryPolicy
		want   []time.Duration
	}{
		{
			name:   "constant",
			policy: middleware.RetryPolicy{Attempts: 4, Delay: time.Second},
			want:   []time.Duration{time.Second, time.Second, time.Second},
		},
		{
			name:   "multiplier",
			policy: middleware.RetryPolicy{Attempts: 4, Delay: time.Second, Multiplier: 2},
			want:   []time.Duration{time.Second, 2 * time.Second, 4 * time.Second},
		},
		{
			name:   "multiplier of 1 or less",
			policy: middleware.RetryPolicy{Attempts: 4, Delay: time.Second, Multiplier: 0.5},
			want:   []time.Duration{time.Second, time.Second, time.Second},
		},
		{
			name:   "max delay",
			policy: middleware.RetryPolicy{Attempts: 4, Delay: time.Second, Multiplier: 2, MaxDelay: 3 * time.Second},
			want:   []time.Duration{time.Second, 2 * time.Second, 3 * time.Second},
		},
		{
			name:   "max delay under delay",
			policy: middleware.RetryPolicy{Attempts: 4, Delay: 5 * time.Second, Multiplier: 2, MaxDelay: 3 * time.Second},
			want:   []time.Duration{3 * time.Second, 3 * time.Second, 3 * time.Second},
		},
		{
			name:   "no delay",
			policy: middleware.RetryPolicy{Attempts: 4, Multiplier: 2},
			want:   []time.Duration{0, 0, 0},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				f := &flaky{errs: []error{attemptError(1), attemptError(2), attemptError(3), attemptError(4)}}

				err := middleware.Retry[int](tt.policy)(f.handle)(t.Context(), uuid.New(), 1)
				if !asIs(err, attemptError(4)) {
					t.Errorf("Retry returned %v, want the error of the last call as is, %v", err, attemptError(4))
				}
				if got := f.gaps(); !slices.Equal(got, tt.want) {
					t.Errorf("Retry waited %v between calls, want %v", got, tt.want)
				}
			})
		})
	}
}

// Once IsRetryable rejects an error, which it gets as the handler returned it, Retry returns that error without calling
// the handler again.
func TestRetry_NotRetryable(t *testing.T) {
	t.Parallel()

	var checked []error
	f := &flaky{errs: []error{attemptError(1), attemptError(2), attemptError(3)}}
	handle := middleware.Retry[int](middleware.RetryPolicy{Attempts: 5, IsRetryable: func(err error) bool {
		checked = append(checked, err)

		return !asIs(err, attemptError(2))
	}})(f.handle)

	if err := handle(t.Context(), uuid.New(), 1); !asIs(err, attemptError(2)) {
		t.Errorf("Retry returned %v, want the error IsRetryable rejected, %v", err, attemptError(2))
	}
	if got, want := checked, []error{attemptError(1), attemptError(2)}; !slices.Equal(got, want) {
		t.Errorf("IsRetryable got %v, want %v", got, want)
	}
	if len(f.calls) != 2 {
		t.Errorf("handler was called %d times, want 2", len(f.calls))
	}
}

// Retry never retries an error matching subscriber.ErrClosed, which a SubscribeSeq loop that has ended returns.
func TestRetry_Closed(t *testing.T) {
	t.Parallel()

	closed := &subscriber.ClosedError[int]{SubscriberID: uuid.New(), Message: 1}
	f := &flaky{errs: []error{closed}}
	handle := middleware.Retry[int](middleware.RetryPolicy{Attempts: 3})(f.handle)

	if err := handle(t.Context(), uuid.New(), 1); !asIs(err, closed) {
		t.Errorf("Retry returned %v, want %v", err, closed)
	}
	if len(f.calls) != 1 {
		t.Errorf("handler was called %d times, want once", len(f.calls))
	}
}

// With Attempts 1 or less, the zero RetryPolicy included, Retry calls the handler once.
func TestRetry_NoRetry(t *testing.T) {
	t.Parallel()

	for _, attempts := range []int{-1, 0, 1} {
		f := &flaky{errs: []error{attemptError(1), attemptError(2)}}
		handle := middleware.Retry[int](middleware.RetryPolicy{Attempts: attempts})(f.handle)

		if err := handle(t.Context(), uuid.New(), 1); !asIs(err, attemptError(1)) {
			t.Errorf("with Attempts %d, Retry returned %v, want %v", attempts, err, attemptError(1))
		}
		if len(f.calls) != 1 {
			t.Errorf("with Attempts %d, handler was called %d times, want once", attempts, len(f.calls))
		}
	}
}

// Jitter draws each wait between the wait times 1 - Jitter and the wait itself, and is clamped to [0, 1].
func TestRetry_Jitter(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		jitter   float64
		min, max time.Duration
	}{
		{jitter: 0.5, min: 500 * time.Millisecond, max: time.Second},
		{jitter: 2, min: 0, max: time.Second},
		{jitter: -1, min: time.Second, max: time.Second},
	} {
		t.Run(fmt.Sprint(tt.jitter), func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				const attempts = 100
				f := &flaky{errs: slices.Repeat([]error{attemptError(1)}, attempts)}
				policy := middleware.RetryPolicy{Attempts: attempts, Delay: time.Second, Jitter: tt.jitter}

				_ = middleware.Retry[int](policy)(f.handle)(t.Context(), uuid.New(), 1)
				gaps := f.gaps()
				for _, gap := range gaps {
					if gap < tt.min || gap > tt.max {
						t.Errorf("Retry waited %v, want between %v and %v", gap, tt.min, tt.max)
					}
				}
				// The odds of 99 equal draws are nil.
				if distinct := len(slices.Compact(slices.Sorted(slices.Values(gaps)))); (tt.min == tt.max) != (distinct == 1) {
					t.Errorf("Retry waited %d different times, want 1 only without jitter", distinct)
				}
			})
		})
	}
}

// Once ctx is done, Retry stops waiting and returns the error of the last call without calling the handler again. It
// still makes the first call.
func TestRetry_ContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		f := &flaky{errs: []error{attemptError(1), attemptError(2)}}
		handle := middleware.Retry[int](middleware.RetryPolicy{Attempts: 3, Delay: time.Hour})(
			func(ctx context.Context, id uuid.UUID, msg int) error {
				time.AfterFunc(time.Second, cancel)

				return f.handle(ctx, id, msg)
			})

		start := time.Now()
		if err := handle(ctx, uuid.New(), 1); !asIs(err, attemptError(1)) {
			t.Errorf("Retry returned %v, want the error of the last call, %v", err, attemptError(1))
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("Retry returned after %v, want as soon as ctx was done, after %v", elapsed, time.Second)
		}
		if len(f.calls) != 1 {
			t.Errorf("handler was called %d times, want once", len(f.calls))
		}
	})

	// Without a delay to wait for, a done ctx still stops the retries.
	f := &flaky{errs: []error{attemptError(1), attemptError(2)}}
	handle := middleware.Retry[int](middleware.RetryPolicy{Attempts: 3})(f.handle)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := handle(ctx, uuid.New(), 1); !asIs(err, attemptError(1)) {
		t.Errorf("with ctx done and no delay, Retry returned %v, want %v", err, attemptError(1))
	}
	if len(f.calls) != 1 {
		t.Errorf("with ctx done and no delay, handler was called %d times, want once", len(f.calls))
	}
}

// asIs reports whether err is want itself, not wrapped: the handler's errors must reach Retry's caller and IsRetryable
// as the handler returned them.
func asIs(err, want error) bool {
	return err == want //nolint:err113,errorlint // A wrapped error would be wrong.
}

// attemptError is what flaky returns, told apart by the call it failed.
type attemptError int

func (e attemptError) Error() string {
	return fmt.Sprintf("attempt %d failed", int(e))
}

// flaky is a handler that returns errs, one per call, and then nil, and records when each call was made.
type flaky struct {
	errs  []error
	calls []time.Time
}

func (f *flaky) handle(context.Context, uuid.UUID, int) error {
	f.calls = append(f.calls, time.Now())
	if len(f.calls) > len(f.errs) {
		return nil
	}

	return f.errs[len(f.calls)-1]
}

// gaps returns the time between each call and the next.
func (f *flaky) gaps() []time.Duration {
	gaps := make([]time.Duration, 0, len(f.calls))
	for i := 1; i < len(f.calls); i++ {
		gaps = append(gaps, f.calls[i].Sub(f.calls[i-1]))
	}

	return gaps
}
