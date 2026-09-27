package broadcastor

import (
	"context"
	"time"
)

// SubscriberOption configures a subscriber when it is passed to Subscribe or SubscribeSeq.
type SubscriberOption[T any] func(subscriber *subscriber[T])

// MessageOptions configures how a Broadcast hands its message to each subscriber, when passed to Broadcast or to
// WithSubscriberDefaultMessageOptions.
type MessageOptions[T any] func(message *message[T])

// WithSubscriberBuffer sets the channel buffer size for the subscriber.
func WithSubscriberBuffer[T any](buffer int) SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		subscriber.ch = make(chan message[T], buffer)
	}
}

// WithSubscriberErrorHandler sets the function called with every error handle returns, with the ctx handle is given.
// Without it, errors are discarded.
func WithSubscriberErrorHandler[T any](handler func(ctx context.Context, err error)) SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		subscriber.errorHandler = handler
	}
}

// WithSubscriberRecover makes the subscriber recover when handle panics, instead of letting the panic crash the program.
// Its error handlers are given a *PanicError with the value handle panicked with and the stack at that point, from the
// subscriber's goroutine and with the ctx handle is given, like a *HandleError. The subscriber then goes on with the
// next message. Only panics in handle are recovered, not those in error handlers.
func WithSubscriberRecover[T any]() SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		subscriber.recover = true
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
// the subscriber it failed on: a *HandleError or a *PanicError when handle fails, with the ctx handle is given and
// from the subscriber's goroutine, so a slow handler holds the subscriber up; a *TimeoutError, a *DroppedError or a
// *SubscriberClosedError when Broadcast could not hand the message over, with the ctx passed to Broadcast. Every
// subscriber shares it, so it may be called concurrently, and after Broadcast has returned.
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
