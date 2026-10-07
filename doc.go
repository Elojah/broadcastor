// Package broadcastor is an in-process fan-out: one Broadcast hands a message to every subscriber. Each subscriber's
// own goroutine calls handle, one message at a time:
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
// SubscribeSeq returns an iterator instead, whose loop body replaces handle, and whose fail takes handle's error:
//
//	_, seq, err := b.SubscribeSeq(ctx)
//	...
//	for msg, fail := range seq {
//		fail(save(msg))
//	}
//
// The options for Subscribe, SubscribeSeq and Unsubscribe are in package subscriber, those for Broadcast in package
// message. examples/ runs each one.
//
// # Delivery
//
// Delivery is at most once. By default Broadcast waits for each subscriber in turn: subscriber.WithBuffer,
// message.WithParallel, message.WithAsync and message.WithNonBlocking change that. Broadcast gives up on a subscriber
// once its ctx is done or the message's timeout runs out (message.WithTimeout, subscriber.WithTimeout).
// subscriber.WithFilter skips messages before any send (see package filter).
//
// # Ordering
//
// A subscriber gets what it replays first (subscriber.WithReplay), then messages in the order Broadcast hands them over:
// in order from one goroutine, except with message.WithAsync, and in no order from several.
//
// # Lifecycle
//
// Unsubscribe and Close never wait, so handle can call them, and a Broadcast waiting on the subscriber gives up. The
// subscriber still handles what it took, unless subscriber.WithUnsubscribeDiscard. It is also unsubscribed once its
// Subscribe ctx is done (unless subscriber.WithDetachedContext), and after too many losses in a row with
// subscriber.WithEvictAfter. subscriber.WithOnDone runs once it is done. Shutdown is Close, then waits for every
// subscriber to be done, or for its ctx.
//
// To reconnect, a subscriber need not leave: handle dials again, middleware.Retry retries the message, and
// subscriber.WithBuffer keeps what comes meanwhile, in order.
//
// # Contexts
//
// The Subscribe ctx is the subscription's lifetime, and what handle and the error handlers get, unless
// message.WithContext replaces it for one message. The Broadcast ctx only bounds the wait, but async sends keep using
// it after Broadcast returns (see message.WithAsync).
//
// # Errors
//
// Errors go to subscriber.WithErrorHandler and message.WithErrorHandler, or are discarded. handle's arrive as is, and a
// missed message as one of package subscriber's error types, each matching a sentinel with errors.Is. Package
// middleware recovers panics, keeps a history, drops stale messages, wraps errors and retries.
//
// # Dead letters
//
// subscriber.WithDeadLetters stores every message the subscriber loses, so each is handled or stored, once. Package
// store holds Ring, an in-memory queue, and Drain, which hands a queue's entries to a handle: with Enqueue as the
// subscriber's handle, that forwards to a slow sink without holding Broadcast up. subscriber.WithReplay hands a
// subscriber values before any message, such as what it lost before a restart.
//
// # Stats
//
// Broadcastor.Stats returns a snapshot of each subscriber's counters (subscriber.Stats).
package broadcastor
