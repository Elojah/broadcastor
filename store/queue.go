package store

import (
	"context"

	"github.com/elojah/broadcastor/subscriber"
)

// Queue is a subscriber.Store that a single reader reads back, oldest first. Put may be called concurrently with it.
type Queue[T any] interface {
	subscriber.Store[T]

	// Next returns the oldest entry not yet acked, waiting for one, or ctx.Err() once ctx is done.
	Next(ctx context.Context) (Entry[T], error)

	// Ack removes the entry with the given ID, and ignores an ID it does not hold.
	Ack(ctx context.Context, id string) error
}

// Entry is a Record with the opaque ID its Queue gave it, for Ack.
type Entry[T any] struct {
	subscriber.Record[T]

	ID string
}
