// Package holder holds ready-made subscriber.Holder for subscriber.WithHolder, which hold the messages a subscriber
// takes for a while before handle gets them, so that a burst reaches handle as fewer messages:
//
//	id, err := b.Subscribe(ctx, handle, subscriber.WithHolder(holder.NewLatest[float64](time.Second)))
//
// Latest keeps only the newest message of a window, Reduce reduces those of a window to fewer. A window starts with
// the first message held, when the subscriber takes it, and ends d later, never earlier but once the subscriber is
// unsubscribed. Each holder keeps state, so give each subscriber its own.
package holder
