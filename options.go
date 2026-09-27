package broadcastor

import (
	"context"
	"time"
)

// SubscriberOption configures a subscriber when it is passed to Subscribe.
type SubscriberOption[T any] func(subscriber *subscriber[T])

// MessageOptions configures how a Broadcast hands its message to each subscriber, when passed to Broadcast or to
// WithSubscriberDefaultMessageOptions.
type MessageOptions[T any] func(message *message[T])

// UnsubscribeOption configures what a subscriber does with the messages it takes once it is unsubscribed, when passed
// to Unsubscribe or to WithSubscriberDefaultUnsubscribeOptions.
type UnsubscribeOption func(unsubscription *unsubscription)

// unsubscription is how a single Unsubscribe or Close removes one subscriber, as set by the subscriber's default
// unsubscribe options and then the Unsubscribe's own.
type unsubscription struct {
	// discard makes the subscriber report the messages it takes from then on instead of processing them.
	discard bool
}

// WithSubscriberBuffer sets the channel buffer size for the subscriber.
func WithSubscriberBuffer[T any](buffer int) SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		subscriber.ch = make(chan message[T], buffer)
	}
}

// WithSubscriberErrorHandler sets the function called with every error handle returns, with the same ctx as handle.
// Without it, errors are discarded.
func WithSubscriberErrorHandler[T any](handler func(ctx context.Context, err error)) SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		subscriber.errorHandler = handler
	}
}

// WithSubscriberRecover makes the subscriber recover when handle panics, instead of letting the panic crash the program.
// Its error handlers are given a *PanicError with the value handle panicked with and the stack at that point, from the
// subscriber's goroutine and with the same ctx as handle, like a *HandleError. The subscriber then goes on with the
// next message. Only panics in handle are recovered, not those in error handlers. It has no effect on SubscribeSeq,
// whose loop body runs in the caller's goroutine.
func WithSubscriberRecover[T any]() SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		subscriber.recover = true
	}
}

// WithSubscriberAutoUnsubscribe unsubscribes the subscriber once the ctx passed to Subscribe or SubscribeSeq is done,
// like an Unsubscribe with no options, so its WithSubscriberDefaultUnsubscribeOptions apply. Without it, cancelling that
// ctx leaves a Subscribe subscriber subscribed, and ends a SubscribeSeq loop only once the loop checks it.
//
// The subscriber is unsubscribed from a goroutine of its own as soon as ctx is done, or right after it is added if ctx
// already is, even while handle or the loop body is running, or before the loop has started: no later Broadcast waits
// for it. As after any Unsubscribe, it may still get messages from its buffer or from a Broadcast that was already
// sending to it, and processes them with the done ctx, unless it discards them. Once the subscriber is unsubscribed
// another way, nothing waits for ctx any more, so ctx may be one that is never done.
func WithSubscriberAutoUnsubscribe[T any]() SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		subscriber.autoUnsubscribe = true
	}
}

// WithSubscriberBroadcastValues makes the values of the ctx passed to Broadcast reach handle, such as a trace ID or a
// request-scoped logger. Without it, handle gets the ctx passed to Subscribe, and nothing from Broadcast. With it, handle
// gets a ctx that has the values of the Broadcast ctx of its message, then those of the Subscribe ctx: a key set on both
// is looked up on the Broadcast ctx.
//
// Its deadline and cancellation are still those of the Subscribe ctx alone, and so is context.Cause. By the time handle
// runs, Broadcast may have returned and its ctx be done, which handle does not see.
//
// Every error reported once the subscriber has taken the message is reported with that same ctx: a *HandleError, a
// *PanicError, or a *SubscriberClosedError when the subscriber discards it or a SubscribeSeq loop ends before yielding
// it. Broadcast still reports the errors about handing the message over with its own ctx. A SubscribeSeq loop body is
// given no ctx, so there only the error handlers get the Broadcast ctx's values.
func WithSubscriberBroadcastValues[T any]() SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		subscriber.broadcastValues = true
	}
}

// WithSubscriberDefaultMessageOptions sets message options applied to every message sent to the subscriber, before the
// options passed to Broadcast, which override them. With WithMessageAsync, for instance, no Broadcast waits for this
// subscriber, which may then receive the messages of successive Broadcasts out of order, unless a Broadcast passes
// WithMessageSync.
func WithSubscriberDefaultMessageOptions[T any](options ...MessageOptions[T]) SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		for _, option := range options {
			option(&subscriber.defaults)
		}
	}
}

// WithSubscriberDefaultUnsubscribeOptions sets unsubscribe options applied whenever the subscriber is unsubscribed,
// before the options passed to Unsubscribe, which override them. They are the only ones Close applies. With
// WithUnsubscribeDiscard, for instance, the subscriber stops processing messages as soon as it is unsubscribed, by
// Unsubscribe or by Close, unless an Unsubscribe passes WithUnsubscribeDeliver.
func WithSubscriberDefaultUnsubscribeOptions[T any](options ...UnsubscribeOption) SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		for _, option := range options {
			option(&subscriber.unsubscribeDefaults)
		}
	}
}

// WithSubscriberTimeout bounds how long every Broadcast waits for the subscriber to take its message, like
// WithMessageTimeout. It is the same as WithSubscriberDefaultMessageOptions(WithMessageTimeout(timeout)), so a Broadcast
// passing WithMessageTimeout overrides it, whether longer, shorter or 0 for none.
func WithSubscriberTimeout[T any](timeout time.Duration) SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		subscriber.defaults.timeout = timeout
	}
}

// WithMessageSync makes Broadcast wait for each subscriber in turn to take the message, until ctx is done or the
// message's timeout runs out. It is the default, so it is only needed to override a subscriber's default of
// WithMessageAsync or WithMessageNonBlocking for one Broadcast. WithMessageSync, WithMessageAsync and
// WithMessageNonBlocking exclude each other: the last one given wins.
func WithMessageSync[T any]() MessageOptions[T] {
	return func(message *message[T]) {
		message.delivery = deliverySync
	}
}

// WithMessageAsync makes Broadcast return right away instead of waiting for each subscriber to take the message: it is
// sent to every subscriber from its own goroutine, until the subscriber takes it, ctx is done or the message's timeout
// runs out. A slow subscriber then holds up neither Broadcast nor the other subscribers, but a subscriber may receive
// the messages of successive Broadcasts out of order.
func WithMessageAsync[T any]() MessageOptions[T] {
	return func(message *message[T]) {
		message.delivery = deliveryAsync
	}
}

// WithMessageNonBlocking makes Broadcast hand the message only to the subscribers that can take it right away, and never
// wait: an unbuffered subscriber must be idle, waiting for its next message, and a buffered one must have room left in
// its buffer. Every other subscriber misses the message, and its error handlers are given a *DroppedError, with the
// ctx passed to Broadcast. Since Broadcast never waits, neither ctx nor the message's timeout plays any part. A slow
// subscriber then holds up nobody, and still gets the messages it does take in order.
func WithMessageNonBlocking[T any]() MessageOptions[T] {
	return func(message *message[T]) {
		message.delivery = deliveryNonBlocking
	}
}

// WithMessageErrorHandler sets a function called with every error about this message, on top of the error handler of
// the subscriber it failed on: a *HandleError or a *PanicError when handle fails, with the same ctx as handle and from
// the subscriber's goroutine, so a slow handler holds the subscriber up; a *TimeoutError, a *DroppedError or a
// *SubscriberClosedError when Broadcast could not hand the message over, with the ctx passed to Broadcast; a
// *SubscriberClosedError when a SubscribeSeq loop ended before yielding it, with the ctx passed to SubscribeSeq, or when
// the subscriber took it after being unsubscribed with WithUnsubscribeDiscard, with the ctx passed to Subscribe or
// SubscribeSeq. With WithSubscriberBroadcastValues, those last two also have the values of the Broadcast ctx, as
// handle's does. Every subscriber shares it, so it may be called concurrently, and after Broadcast has returned.
func WithMessageErrorHandler[T any](handler func(ctx context.Context, err error)) MessageOptions[T] {
	return func(message *message[T]) {
		message.errorHandler = handler
	}
}

// WithMessageTimeout bounds how long Broadcast waits for each subscriber to take the message, on top of ctx. Once it
// runs out, that subscriber misses the message, and its error handlers are given a *TimeoutError wrapping
// context.DeadlineExceeded, with the ctx passed to Broadcast. Unlike a ctx deadline, which a synchronous Broadcast uses up
// across all subscribers, each subscriber gets the whole timeout, counted from when Broadcast gets to it: a synchronous
// Broadcast can then take up to the timeout for each subscriber. A timeout of 0 or less means none, which overrides
// WithSubscriberTimeout.
func WithMessageTimeout[T any](timeout time.Duration) MessageOptions[T] {
	return func(message *message[T]) {
		message.timeout = timeout
	}
}

// WithUnsubscribeDiscard makes the subscriber stop processing messages once it is unsubscribed. Every message it takes
// from then on, from its buffer or from a Broadcast that was already sending to it, is reported to its error handlers
// as a *SubscriberClosedError instead of being passed to handle, with the same ctx as handle. A SubscribeSeq loop ends
// instead of yielding it, and reports it with the ctx passed to SubscribeSeq, plus the values of the Broadcast ctx with
// WithSubscriberBroadcastValues. Unsubscribe still does not wait: handle, or the loop body, may be running or about to
// start for one message when it returns, but for no other one. It has no effect when the subscriber was already
// unsubscribed, by another Unsubscribe or by Close, whose options applied instead. WithUnsubscribeDiscard and
// WithUnsubscribeDeliver exclude each other: the last one given wins.
func WithUnsubscribeDiscard() UnsubscribeOption {
	return func(unsubscription *unsubscription) {
		unsubscription.discard = true
	}
}

// WithUnsubscribeDeliver makes the subscriber process every message it takes once it is unsubscribed, from its buffer
// or from a Broadcast that was already sending to it, as if it were still subscribed. It is the default, so it is only
// needed to override a subscriber's default of WithUnsubscribeDiscard for one Unsubscribe.
func WithUnsubscribeDeliver() UnsubscribeOption {
	return func(unsubscription *unsubscription) {
		unsubscription.discard = false
	}
}
