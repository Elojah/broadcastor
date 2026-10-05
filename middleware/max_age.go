package middleware

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/subscriber"
)

// MaxAge does not hand the handler it wraps a message older than d, since a reading or a command that waited behind a
// slow handle may be worse than none. at returns when the message was made, since the library stamps none. It returns
// a *subscriber.ExpiredError instead, so the message counts as Failed and reaches the dead letters like any handle
// error. 0 or less means no limit.
//
// Put MaxAge after History, which then does not record an expired message, and before WrapError and Retry: its error
// already names the subscriber and the message, and an expired message is not retried.
func MaxAge[T any](d time.Duration, at func(msg T) time.Time) subscriber.Middleware[T] {
	return func(next subscriber.Handler[T]) subscriber.Handler[T] {
		if d <= 0 {
			return next
		}

		return func(ctx context.Context, id uuid.UUID, msg T) error {
			if age := time.Since(at(msg)); age > d {
				return &subscriber.ExpiredError[T]{SubscriberID: id, Message: msg, Age: age}
			}

			return next(ctx, id, msg)
		}
	}
}
