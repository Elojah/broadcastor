package middleware

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/subscriber"
)

// MaxAge returns a *subscriber.ExpiredError instead of handing the wrapped handler a message older than d. at returns
// when the message was made, since the library stamps none. An expired message counts as Failed and reaches the dead
// letters. d <= 0 means no limit.
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
