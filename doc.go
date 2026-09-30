// Package broadcastor is an in-process fan-out: one Broadcast call hands a message to every subscriber.
//
// Each subscriber has its own goroutine, which calls handle for each message, one at a time:
//
//	b := broadcastor.NewBroadcastor[string]()
//	id, err := b.Subscribe(ctx, func(ctx context.Context, _ uuid.UUID, msg string) error {
//		fmt.Println(msg)
//		return nil
//	})
//	...
//	b.Broadcast(ctx, "hello")
//	...
//	err = b.Unsubscribe(ctx, id)
//
// SubscribeSeq returns an iterator instead, whose loop body takes the place of handle:
//
//	_, seq, err := b.SubscribeSeq(ctx)
//	...
//	for msg := range seq {
//		fmt.Println(msg)
//	}
//
// The options passed to Subscribe, SubscribeSeq and Unsubscribe are in package subscriber, and those passed to
// Broadcast in package message. The README covers what follows in more detail.
//
// # Delivery
//
// Delivery is at most once. By default Broadcast waits for each subscriber in turn to take the message, so a slow one
// holds up those after it. subscriber.WithBuffer, message.WithParallel, message.WithAsync and message.WithNonBlocking
// change that. Broadcast gives up on a subscriber once its ctx is done or the message's timeout runs out
// (message.WithTimeout, subscriber.WithTimeout).
//
// # Unsubscribing
//
// Unsubscribe and Close never wait, so handle can call them, and a Broadcast waiting on a subscriber gives up once it is
// unsubscribed. A subscriber may still process messages it already took, unless unsubscribed with
// subscriber.WithUnsubscribeDiscard. Once the ctx passed to Subscribe is done, the subscriber
// is unsubscribed, unless it has subscriber.WithDetachedContext.
//
// # Contexts
//
// The Subscribe ctx is the subscription's lifetime, and what handle and the error handlers get. The Broadcast ctx only
// bounds the wait, but async sends keep using it after Broadcast returns (see message.WithAsync). message.WithContext
// replaces the Subscribe ctx for one message.
//
// # Errors
//
// Errors go to subscriber.WithErrorHandler and message.WithErrorHandler, and are discarded without one. handle's
// errors arrive as is. Package middleware can recover panics (Recover), wrap errors (WrapError) and retry (Retry).
// Broadcast reports a message it could not hand over as a *subscriber.TimeoutError, *subscriber.DroppedError or
// *subscriber.ClosedError. Each error type matches a sentinel with errors.Is.
//
// # Storing lost messages
//
// subscriber.WithStore gives a subscriber.Store every message the subscriber loses, so every message is either handled
// or stored, once. Package store holds store.Ring, an in-memory queue to read them back from, and store.Drain, which
// hands them to a handle again. With store.Enqueue as the handle, it forwards messages to a slow sink without holding
// Broadcast up.
//
// # Stats
//
// Broadcastor.Stats returns a snapshot of each subscriber's counters (subscriber.Stats): how many messages are queued,
// and how many it took, handled, failed on, and missed by timeout or drop, with the time spent in handle.
package broadcastor
