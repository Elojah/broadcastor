package middleware

import (
	"context"
	"runtime/debug"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/subscriber"
)

// Recover turns a panic in the handler it wraps into a *subscriber.PanicError, and the subscriber goes on with the next
// message. Panics in outer middlewares and in error handlers are not recovered.
func Recover[T any]() subscriber.Middleware[T] {
	return func(next subscriber.Handler[T]) subscriber.Handler[T] {
		return func(ctx context.Context, id uuid.UUID, msg T) (err error) {
			defer func() {
				if v := recover(); v != nil {
					err = &subscriber.PanicError[T]{SubscriberID: id, Message: msg, Value: v, Stack: debug.Stack()}
				}
			}()

			return next(ctx, id, msg)
		}
	}
}
