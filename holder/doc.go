// Package holder holds ready-made subscriber.Holder for subscriber.WithHolder, which hold the messages a subscriber
// takes for a while before handle gets them, so that a burst reaches handle as fewer messages:
//
//	id, err := b.Subscribe(ctx, handle, subscriber.WithHolder(holder.NewLatest[float64](time.Second)))
//
// Latest keeps only the newest message of a window, Reduce reduces those of a window to fewer. A window starts when
// the subscriber takes its first message, and ends d later, or as soon as the subscriber is unsubscribed. Each holder
// keeps state, so give each subscriber its own.
package holder
