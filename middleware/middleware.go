// Package middleware holds ready-made subscriber.Middleware, to pass to subscriber.WithMiddleware:
//
//	id, err := b.Subscribe(ctx, handle, subscriber.WithMiddleware(
//		middleware.Recover[string](),
//		middleware.WrapError[string](),
//	))
//
// Given first, in that order, Recover also recovers panics in every later middleware, and the *subscriber.PanicError it
// returns is not wrapped in a *subscriber.HandleError.
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
