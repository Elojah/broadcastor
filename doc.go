// Package broadcastor is an in-process fan-out: one Broadcast call hands a message to every subscriber.
//
// Each subscriber has its own goroutine, which calls the handle function given to Subscribe for every message it
// takes, one at a time, with the subscriber's ID:
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
// SubscribeSeq adds a subscriber too, but returns an iterator instead of taking a handle function. The loop body then
// takes the place of handle, in the caller's goroutine:
//
//	_, seq, err := b.SubscribeSeq(ctx)
//	...
//	for msg := range seq {
//		fmt.Println(msg)
//	}
//
// The options live next to what they configure: package subscriber holds those passed to Subscribe, SubscribeSeq and
// Unsubscribe, along with Handler, Middleware and the errors about a subscriber's messages, and package message those
// passed to Broadcast.
//
// # Delivery
//
// Delivery is at most once: a subscriber gets a message once or misses it, and a missed message is never sent again,
// although subscriber.WithStore keeps it so that it can be handled later (see Storing lost messages below).
// How Broadcast hands a message over depends on the message options, which apply to every subscriber, and on each
// subscriber's defaults (subscriber.WithDefaultMessageOptions):
//
//   - Sync, the default (message.WithSync): Broadcast goes through the subscribers one at a time, and waits for each to
//     take the message. A subscriber only takes its next message once handle has returned, so a slow subscriber holds
//     up Broadcast and every subscriber after it. A subscriber gets the messages of successive Broadcasts from one
//     goroutine in order.
//   - Buffered (subscriber.WithBuffer): as above, but Broadcast only waits for the subscriber once its buffer is full.
//   - Parallel (message.WithParallel): Broadcast sends the message to every subscriber at once, each from its own
//     goroutine, and returns once each has taken it or missed it. A slow subscriber then holds up Broadcast but nobody
//     else, and a subscriber still gets the messages of successive Broadcasts from one goroutine in order.
//   - Async (message.WithAsync): Broadcast sends the message to each subscriber from its own goroutine, and returns
//     right away. A slow subscriber then holds up nobody else, but may get the messages of successive Broadcasts out of
//     order.
//   - Non-blocking (message.WithNonBlocking): Broadcast never waits. A subscriber that cannot take the message right
//     away, because it is busy in handle or its buffer is full, misses it.
//
// While waiting, Broadcast gives up on a subscriber once the Broadcast ctx is done, or once the message's timeout
// (message.WithTimeout, or subscriber.WithTimeout for every message sent to a subscriber) runs out. The timeout is
// counted separately for each subscriber, from when Broadcast gets to it, while a ctx deadline is used up across all
// of them. A parallel Broadcast gets to every subscriber at once, so both apply to all of them from the same moment.
//
// Broadcast returns how many subscribers it handed the message to.
//
// # Unsubscribing
//
// Unsubscribe never waits for anything, so handle can unsubscribe its own subscriber. A subscriber may still get
// messages after Unsubscribe returns: whatever is in its buffer, and the message of a Broadcast that was already
// sending to it. Its goroutine ends once it has processed them. The ctx given to Subscribe, which is the one handle
// gets unless a message has its own, is the subscription's: the subscriber is unsubscribed as soon as that ctx is done,
// even while handle is running. subscriber.WithDetachedContext keeps it subscribed instead, and hands handle a ctx with
// the same values that is never done.
//
// A SubscribeSeq loop ends once its subscriber is unsubscribed and has yielded those messages. The loop unsubscribes
// the subscriber itself when it ends another way: when it breaks, or once the ctx given to SubscribeSeq is done. The
// messages the subscriber took but did not yield are then reported as *subscriber.ClosedError.
//
// Close unsubscribes every subscriber the same way, and from then on Subscribe and SubscribeSeq return ErrClosed, so
// Broadcast reaches nobody. It never waits either, so handle can call it too, and it is safe to call concurrently with
// anything, including another Close: the first returns nil, and every later one ErrClosed.
//
// With subscriber.WithUnsubscribeDiscard, a subscriber stops processing messages once it is unsubscribed: those it
// takes from then on are reported as *subscriber.ClosedError instead of being passed to handle, and a SubscribeSeq loop
// ends right away. Pass it to Unsubscribe, or make it the subscriber's default with
// subscriber.WithDefaultUnsubscribeOptions, which is the only way Close applies it.
//
// # Contexts
//
// Each ctx has one job:
//
//   - The ctx given to Subscribe or SubscribeSeq is the subscription's lifetime, as above, and what every message is
//     handled with by default: handle and its middlewares get it, and so do the error handlers with every error about
//     the subscriber's messages.
//   - The ctx given to Broadcast only bounds how long Broadcast waits for each subscriber to take the message. Its
//     values reach neither handle nor the error handlers.
//   - The ctx given to message.WithContext replaces the subscription's for one message: handle, its middlewares and the
//     error handlers get it as is, with its values and its cancellation. Pass context.WithoutCancel(ctx) to keep only
//     its values, since a buffered or async subscriber may take the message once ctx is done.
//
// An async send keeps using the Broadcast ctx after Broadcast has returned, so a ctx that is done once the caller
// returns, such as a request's, makes every subscriber that has not taken the message by then miss it. To let the
// sends outlive the caller, detach them and bound them with a timeout instead:
//
//	detached := context.WithoutCancel(ctx)
//	b.Broadcast(detached, msg, message.WithAsync[T](), message.WithTimeout[T](time.Second), message.WithContext[T](detached))
//
// # Middleware
//
// subscriber.WithMiddleware wraps handle in middlewares, the first one outermost. Each is a subscriber.Middleware,
// which takes the next subscriber.Handler and returns one that may act before or after calling it, change its error,
// call it again, or not call it at all:
//
//	logged := func(next subscriber.Handler[string]) subscriber.Handler[string] {
//		return func(ctx context.Context, id uuid.UUID, msg string) error {
//			err := next(ctx, id, msg)
//			log.Println(id, msg, err)
//			return err
//		}
//	}
//	id, err := b.Subscribe(ctx, handle, subscriber.WithMiddleware(
//		middleware.Recover[string](),
//		middleware.WrapError[string](),
//		logged,
//	))
//
// Middlewares run in the subscriber's goroutine, with the ctx handle gets, and have no effect on SubscribeSeq.
// Package middleware holds ready-made ones: Recover and WrapError, which go first, in that order, and Retry, which
// calls handle again after an error, with backoff, and goes right after them.
//
// # Errors
//
// Errors are reported to error handlers, and discarded when there are none. A subscriber's handler
// (subscriber.WithErrorHandler) gets every error about that subscriber, and a message's handler
// (message.WithErrorHandler) every error about that message, after the subscriber's. The errors, all in package
// subscriber, are:
//
//   - The error handle returns, as is, or the one its outermost middleware returns: a *subscriber.HandleError, which
//     tells the subscriber and the message, with middleware.WrapError, and a *subscriber.PanicError when handle or a
//     middleware panics, with middleware.Recover (without it, the panic crashes the program). The handlers are called
//     from the subscriber's goroutine, right after handle.
//   - *subscriber.TimeoutError, *subscriber.DroppedError or *subscriber.ClosedError when Broadcast could not hand the
//     message over.
//   - *subscriber.ClosedError when a SubscribeSeq loop ended before yielding a message its subscriber took, or when a
//     subscriber unsubscribed with subscriber.WithUnsubscribeDiscard took a message.
//   - *subscriber.StoreError instead of any of the above when the subscriber's store failed to store the message (see
//     below). It still matches the error it replaces with errors.Is and errors.As.
//
// Both handlers get every error with the ctx the message is handled with: the one message.WithContext gave it, or else
// the subscription's, never the one given to Broadcast. Unless the message has its own, that ctx is done only once the
// subscription is over, even when the error is Broadcast giving up because its own ctx is done.
//
// Each error type the library makes matches a sentinel (subscriber.ErrTimeout, subscriber.ErrDropped, and so on) with
// errors.Is, which does not need to know T.
//
// # Storing lost messages
//
// subscriber.WithStore gives a subscriber.Store every message the subscriber loses, as a subscriber.Record: the
// subscriber's ID, the message, and the error about it, whatever it is among the above. So every message a Broadcast
// picks the subscriber up for is either handled or stored, once, and the store can be read later to handle the stored
// ones again. With subscriber.WithUnsubscribeDiscard as the subscriber's default, Close then stores whatever the
// subscriber had not handled yet:
//
//	id, err := b.Subscribe(ctx, handle,
//		subscriber.WithStore[string](store),
//		subscriber.WithDefaultUnsubscribeOptions[string](subscriber.WithUnsubscribeDiscard()),
//	)
//
// The store's Put is called right before the error handlers, from wherever they are, including from Broadcast for the
// messages it could not hand over. So a slow Put holds things up like a slow error handler, and it may be called from
// several goroutines at once. Its ctx has the values of the ctx the message is handled with, but is never done, since
// that ctx being done is often why the message was lost.
package broadcastor
