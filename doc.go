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
// SubscribeSeq returns an iterator instead, whose loop body takes the place of handle. Each message comes with fail,
// which takes the error handle would return:
//
//	_, seq, err := b.SubscribeSeq(ctx)
//	...
//	for msg, fail := range seq {
//		fail(save(msg))
//	}
//
// The options passed to Subscribe, SubscribeSeq and Unsubscribe are in package subscriber, and those passed to
// Broadcast in package message. Each option's documentation details what follows, and examples/ shows each one in a
// runnable program.
//
// # Delivery
//
// Delivery is at most once. By default Broadcast waits for each subscriber in turn to take the message, so a slow one
// holds up those after it. subscriber.WithBuffer, message.WithParallel, message.WithAsync and message.WithNonBlocking
// change that, and subscriber.WithAsyncLimit bounds the async sends to a subscriber. Broadcast gives up on a subscriber
// once its ctx is done or the message's timeout runs out (message.WithTimeout, subscriber.WithTimeout).
// subscriber.WithFilter skips messages before any send, so the subscriber never wakes up for them, and package filter
// holds filters for readings that repeat themselves.
//
// # Ordering
//
// A subscriber takes messages in the order Broadcast hands them over. Those broadcast from one goroutine keep their
// order, except with message.WithAsync, and those from several goroutines have none.
//
// # Unsubscribing
//
// Unsubscribe and Close never wait, so handle can call them, and a Broadcast waiting on a subscriber gives up once it is
// unsubscribed. A subscriber may still process messages it already took, unless unsubscribed with
// subscriber.WithUnsubscribeDiscard. Once the ctx passed to Subscribe is done, the subscriber is unsubscribed, unless it
// has subscriber.WithDetachedContext. subscriber.WithEvictAfter unsubscribes a subscriber that loses too many messages
// in a row.
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
// errors arrive as is, and a message a subscriber misses as one of package subscriber's error types, each matching a
// sentinel with errors.Is. Package middleware can recover panics (Recover), keep a history (History), drop stale
// messages (MaxAge), wrap errors (WrapError) and retry (Retry).
//
// # Dead letters
//
// subscriber.WithDeadLetters gives a subscriber.Store every message the subscriber loses, so every message is either
// handled or stored, once. Package store holds store.Ring, an in-memory queue to read them back from, and store.Drain,
// which hands them to a handle again. With store.Enqueue as the handle, it forwards messages to a slow sink without
// holding Broadcast up.
//
// # Stats
//
// Broadcastor.Stats returns a snapshot of each subscriber's counters (subscriber.Stats): how many messages are queued
// or being sent async, and how many it took, handled, failed on, and missed by timeout or drop, with the time spent in
// handle, and how long handle has been running on the current message.
package broadcastor
