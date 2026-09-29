// Package store holds ready-made subscriber.Store implementations, for subscriber.WithStore, to read lost messages back
// from:
//
//	lost := store.NewRing[string](1024)
//	id, err := b.Subscribe(ctx, handle, subscriber.WithStore[string](lost))
//	...
//	entry, err := lost.Next(ctx)
//	...
//	err = lost.Ack(ctx, entry.ID)
//
// Ring is an in-memory Queue.
package store
