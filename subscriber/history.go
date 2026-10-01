package subscriber

import "context"

// History keeps the messages a Broadcastor broadcasts, so that a new subscriber can handle them first (WithReplay).
// Give one to broadcastor.WithHistory. Package store holds store.History, which keeps the last ones in memory. Its
// methods are called concurrently, and the library holds no lock around them.
//
// The library numbers the messages itself, and waits until every Append up to the newest message a subscriber should
// replay has returned before calling Read for it. So a replaying subscriber gets each message once, either read back
// or live, with none missed in between, as long as Read returns every message Append was given and the history still
// keeps.
type History[T any] interface {
	// Append keeps msg under offset, which goes up with each message broadcast, from 1. Broadcast calls it with its own
	// ctx before handing msg to anyone, so a slow one holds Broadcast up, and concurrent Broadcasts may call it with
	// offsets out of order. It cannot fail: a message it does not keep is broadcast all the same, but never replayed.
	Append(ctx context.Context, offset uint64, msg T)

	// Read returns the messages kept, in any order. A replaying subscriber calls it once, from its own goroutine, with
	// its ctx, as soon as it is subscribed. If it fails, the subscriber's error handler gets a *ReplayError, and the
	// subscriber handles only the messages broadcast after it subscribed.
	Read(ctx context.Context) ([]HistoryEntry[T], error)
}

// HistoryEntry is a message a History keeps, with the offset Append was given for it.
type HistoryEntry[T any] struct {
	Offset  uint64
	Message T
}
