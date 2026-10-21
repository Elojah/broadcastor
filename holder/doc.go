// Package holder holds ready-made subscriber.Holder for subscriber.WithHolder, which hold the messages a subscriber
// takes for a while before handle gets them, so that a burst reaches handle as fewer messages, or in order:
//
//	id, err := g.Subscribe(ctx, handle, subscriber.WithHolder(holder.NewLatest[float64](time.Second)))
//
// Reduce reduces the messages of a window to fewer, NewLatest to the newest only, Reorder sorts them, and Batch gathers
// them into one batch, for the subscriber's batch handler, ending a window early once it holds n. A window starts when
// the subscriber takes its first message, and ends d later, or as soon as the subscriber is unsubscribed.
// Jitter has no windows: it holds each message for d, sorted with those it holds. Each holder keeps state, so give each
// subscriber its own.
package holder
