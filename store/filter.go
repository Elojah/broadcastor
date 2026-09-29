package store

import (
	"context"

	"github.com/elojah/broadcastor/subscriber"
)

// PutFunc is a func used as a subscriber.Store.
type PutFunc[T any] func(ctx context.Context, r subscriber.Record[T]) error

// Put calls f.
func (f PutFunc[T]) Put(ctx context.Context, r subscriber.Record[T]) error {
	return f(ctx, r)
}

// Filter returns a store that puts in s only the records keep accepts, such as those not failing for good, and ignores
// the others.
func Filter[T any](s subscriber.Store[T], keep func(r subscriber.Record[T]) bool) PutFunc[T] {
	return func(ctx context.Context, r subscriber.Record[T]) error {
		if !keep(r) {
			return nil
		}

		return s.Put(ctx, r)
	}
}
