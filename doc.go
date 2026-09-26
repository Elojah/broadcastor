// Package broadcastor is an in-process fan-out: one Broadcast call hands a message to every subscriber.
//
// Each subscriber has its own goroutine, which calls the handle function given to Subscribe for every message it
// takes, one at a time:
//
//	b := broadcastor.NewBroadcastor[string]()
//	id, err := b.Subscribe(ctx, func(ctx context.Context, msg string) error {
//		fmt.Println(msg)
//		return nil
//	})
//	...
//	b.Broadcast(ctx, "hello")
//	...
//	err = b.Unsubscribe(ctx, id)
//
// # Delivery
//
// Delivery is at most once: a subscriber gets a message once or misses it, and a missed message is never sent again.
// How Broadcast hands a message over depends on the message options, which apply to every subscriber, and on each
// subscriber's defaults (WithSubscriberDefaultMessageOptions):
//
//   - Sync, the default (WithMessageSync): Broadcast goes through the subscribers one at a time, and waits for each to
//     take the message. A subscriber only takes its next message once handle has returned, so a slow subscriber holds
//     up Broadcast and every subscriber after it. A subscriber gets the messages of successive Broadcasts from one
//     goroutine in order.
//   - Buffered (WithSubscriberBuffer): as above, but Broadcast only waits for the subscriber once its buffer is full.
//   - Async (WithMessageAsync): Broadcast sends the message to each subscriber from its own goroutine, and returns
//     right away. A slow subscriber then holds up nobody else, but may get the messages of successive Broadcasts out of
//     order.
//   - Non-blocking (WithMessageNonBlocking): Broadcast never waits. A subscriber that cannot take the message right
//     away, because it is busy in handle or its buffer is full, misses it.
//
// While waiting, Broadcast gives up on a subscriber once the Broadcast ctx is done, or once the message's timeout
// (WithMessageTimeout, or WithSubscriberTimeout for every message sent to a subscriber) runs out. The timeout is counted
// separately for each subscriber, from when Broadcast gets to it, while a ctx deadline is used up across all of them.
//
// Broadcast returns how many subscribers it handed the message to.
//
// # Unsubscribing
//
// Unsubscribe never waits for anything, so handle can unsubscribe its own subscriber. A subscriber may still get
// messages after Unsubscribe returns: whatever is in its buffer, and the message of a Broadcast that was already
// sending to it. Its goroutine ends once it has processed them. Cancelling the ctx given to Subscribe, which is the one
// handle gets, does not unsubscribe it.
//
// # Errors
//
// Errors are reported to error handlers, and discarded when there are none. A subscriber's handler
// (WithSubscriberErrorHandler) gets every error about that subscriber, and a message's handler (WithMessageErrorHandler)
// every error about that message, after the subscriber's. The errors are:
//
//   - *HandleError when handle returns an error, and *PanicError when it panics in a subscriber with
//     WithSubscriberRecover (without it, the panic crashes the program). The handlers are called from the subscriber's
//     goroutine, right after handle, with the ctx given to Subscribe.
//   - *TimeoutError, *DroppedError or *SubscriberClosedError when Broadcast could not hand the message over. The
//     handlers are called with the ctx given to Broadcast.
//
// Each error type matches a sentinel (ErrTimeout, ErrDropped, and so on) with errors.Is, which does not need to know T.
package broadcastor
