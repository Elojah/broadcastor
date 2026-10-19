package middleware_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elojah/broadcastor/v2/middleware"
	"github.com/elojah/broadcastor/v2/subscriber"
)

// The messages are when they were made, and the tests run in a synctest bubble, whose fake clock sets their age exactly.

// MaxAge hands the handler a message no older than d, and returns a *subscriber.ExpiredError for an older one, without
// calling the handler.
func TestMaxAge(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		id := uint64(7)
		made := time.Now()
		calls := 0
		handle := middleware.MaxAge(time.Second, identity)(func(context.Context, uint64, time.Time) error {
			calls++

			return nil
		})

		time.Sleep(time.Second)
		if err := handle(t.Context(), id, made); err != nil || calls != 1 {
			t.Errorf("for a message d old, MaxAge returned %v after %d calls, want nil after 1", err, calls)
		}

		time.Sleep(time.Nanosecond)
		err := handle(t.Context(), id, made)
		var expired *subscriber.ExpiredError[time.Time]
		if !errors.As(err, &expired) || !errors.Is(err, subscriber.ErrExpired) {
			t.Fatalf("for a message older than d, MaxAge returned %v, want a *subscriber.ExpiredError", err)
		}
		if want := time.Second + time.Nanosecond; expired.SubscriberID != id || !expired.Message.Equal(made) || expired.Age != want {
			t.Errorf("*ExpiredError is for subscriber %d and message %v, %v old, want %d, %v and %v",
				expired.SubscriberID, expired.Message, expired.Age, id, made, want)
		}
		if calls != 1 {
			t.Errorf("handler was called %d times, want once, not for the expired message", calls)
		}
	})
}

// MaxAge with 0 or less hands the handler every message, however old.
func TestMaxAge_Zero(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -time.Second} {
		calls := 0
		handle := middleware.MaxAge(d, identity)(func(context.Context, uint64, time.Time) error {
			calls++

			return nil
		})
		if err := handle(t.Context(), 1, time.Time{}); err != nil || calls != 1 {
			t.Errorf("MaxAge(%v) returned %v after %d calls, want nil after 1", d, err, calls)
		}
	}
}

// identity is when a message made of its own time was made.
func identity(made time.Time) time.Time {
	return made
}
