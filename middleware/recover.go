package middleware

import (
	"context"
	"runtime/debug"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/subscriber"
)

// Recover makes a panic in the handler it wraps a *subscriber.PanicError, with the value it panicked with and the stack
// at that point, instead of letting the panic crash the program. The subscriber's error handlers are given it like any
// other error, and the subscriber then goes on with the next message. Only panics in the handler it wraps are
// recovered: not those in outer middlewares, nor in error handlers.
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
