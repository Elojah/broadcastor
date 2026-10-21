// Package store holds subscriber.Store implementations, for subscriber.WithDeadLetters and middleware.History, that
// messages are read back from:
//
//	lost := store.NewRing[string](1024)
//	id, err := g.Subscribe(ctx, handle, subscriber.WithDeadLetters[string](lost))
//	...
//	entry, err := lost.Next(ctx)
//	...
//	err = lost.Ack(ctx, entry.ID)
//
// Ring is an in-memory Queue, File one on local disk, which outlives the process. Drain runs that loop: with Enqueue as
// the subscriber's handle, it forwards every message to a slow sink, in order. Filter keeps some records out of a store.
package store
