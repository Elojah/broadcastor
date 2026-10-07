package middleware

import (
	"context"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/subscriber"
)

// History puts every message the wrapped handler handles in s, with a nil Err. With subscriber.WithDeadLetters(s) too,
// s gets each message once, handled or lost.
//
// Put gets a ctx never done, and runs in the subscriber's goroutine. Its error is returned, so the error handlers and
// dead letters get it although handle succeeded.
func History[T any](s subscriber.Store[T]) subscriber.Middleware[T] {
	return func(next subscriber.Handler[T]) subscriber.Handler[T] {
		return func(ctx context.Context, id uuid.UUID, msg T) error {
			if err := next(ctx, id, msg); err != nil {
				return err
			}

			return s.Put(context.WithoutCancel(ctx), subscriber.Record[T]{SubscriberID: id, Message: msg})
		}
	}
}
