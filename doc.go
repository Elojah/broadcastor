// Package broadcastor is an in-process fan-out: Broadcast hands a message to every subscriber, and each subscriber
// handles its messages one at a time, in its own goroutine.
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
// SubscribeSeq returns an iterator instead, whose loop body replaces handle:
//
//	_, seq, err := b.SubscribeSeq(ctx)
//	...
//	for msg, fail := range seq {
//		fail(save(msg))
//	}
//
// Subscriber options are in package subscriber, Broadcast options in package message. Packages middleware, store and
// filter hold ready-made middlewares, stores and filters.
//
// # Delivery
//
// Delivery is at most once. By default Broadcast waits for each subscriber in turn, until it takes the message, the
// Broadcast ctx is done or the message's timeout runs out. subscriber.WithBuffer, message.WithParallel,
// message.WithAsync and message.WithNonBlocking wait less. Messages broadcast from one goroutine arrive in order,
// except with message.WithAsync.
//
// # Lifecycle
//
// Unsubscribe and Close never wait, so handle can call them. An unsubscribed subscriber still handles what it took,
// unless discarding (subscriber.WithUnsubscribeDiscard). A subscriber is also unsubscribed once its Subscribe ctx is
// done, and when subscriber.WithEvict evicts it. Shutdown is Close, then waits until every subscriber is done.
//
// # Contexts
//
// The Subscribe ctx is the subscription's lifetime, and what handle and the error handlers get, unless
// message.WithContext replaces it for one message. The Broadcast ctx only bounds the wait, but async sends keep using
// it after Broadcast returns.
//
// # Errors
//
// Every error about a message goes to the error handlers, or is discarded: handle's as is, a missed message's as one of
// package subscriber's error types, each matching a sentinel with errors.Is. subscriber.WithDeadLetters also stores
// every message the subscriber loses.
package broadcastor
