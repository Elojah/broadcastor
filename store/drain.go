package store

import (
	"context"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/subscriber"
)

// Drain hands each entry of q to handle, in order, then acks it, until ctx is done or q fails. A failed entry goes to
// deadLetters, or is dropped if nil, unless ctx is done by then: it then stays in q for the next Drain. Wrap handle in
// middleware.Retry to retry.
//
// deadLetters.Put and Ack get a ctx never done, so that an entry handled as ctx ends is not handled again.
func Drain[T any](
	ctx context.Context, q Queue[T], handle subscriber.Handler[T], deadLetters subscriber.Store[T],
) error {
	for {
		entry, err := q.Next(ctx)
		if err != nil {
			return err
		}
		if err := handle(ctx, entry.SubscriberID, entry.Message); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if deadLetters != nil {
				dead := subscriber.Record[T]{SubscriberID: entry.SubscriberID, Message: entry.Message, Err: err}
				if err := deadLetters.Put(context.WithoutCancel(ctx), dead); err != nil {
					return err
				}
			}
		}
		if err := q.Ack(context.WithoutCancel(ctx), entry.ID); err != nil {
			return err
		}
	}
}

// Enqueue returns a handle that only puts each message in q, for Drain to hand on: the subscriber seldom holds
// Broadcast up, and the real handle gets every message in order, from one goroutine. Put gets a ctx never done, and its
// error is handle's.
func Enqueue[T any](q subscriber.Store[T]) subscriber.Handler[T] {
	return func(ctx context.Context, id uuid.UUID, msg T) error {
		return q.Put(context.WithoutCancel(ctx), subscriber.Record[T]{SubscriberID: id, Message: msg})
	}
}
