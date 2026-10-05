package middleware

import (
	"context"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/subscriber"
)

// History puts every message the handler it wraps handles in s, with a nil Err, and returns the handler's error as is.
// Given s too, subscriber.WithDeadLetters puts every message the subscriber loses, so s gets each message once, with
// the error about it if it was lost.
//
// Put gets the ctx's values but a ctx never done, since the message was handled however ctx ended. It runs in the
// subscriber's goroutine, like a slow handle. If it fails, History returns its error, so the error handlers and the
// dead letters get it although the handler succeeded. Put History outside WrapError and Retry, so that error is
// neither a *subscriber.HandleError nor retried, which would handle the message again.
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
