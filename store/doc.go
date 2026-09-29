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
// Ring is an in-memory Queue. Drain runs that loop, and with Enqueue as the subscriber's handle, forwards every message
// to a slow sink in order. Filter keeps some records out of a store.
package store
