package middleware

import (
	"context"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/subscriber"
)

// WrapError makes any error the handler it wraps returns a *subscriber.HandleError, with the subscriber and the message
// it failed on, so that an error handler can tell them apart. That matters most for a message's error handler, which
// every subscriber shares.
func WrapError[T any]() subscriber.Middleware[T] {
	return func(next subscriber.Handler[T]) subscriber.Handler[T] {
		return func(ctx context.Context, id uuid.UUID, msg T) error {
			if err := next(ctx, id, msg); err != nil {
				return &subscriber.HandleError[T]{SubscriberID: id, Message: msg, Err: err}
			}

			return nil
		}
	}
}
