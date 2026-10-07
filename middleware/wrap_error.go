package middleware

import (
	"context"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/subscriber"
)

// WrapError wraps every error in a *subscriber.HandleError naming the subscriber and the message, which a message's
// error handler, shared by every subscriber, needs to tell them apart.
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
