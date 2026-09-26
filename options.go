package broadcastor

import (
	"context"
	"time"
)

type SubscriberOption[T any] func(subscriber *subscriber[T])

type MessageOptions[T any] func(message *message[T])

// WithSubscriberBuffer sets the channel buffer size for the subscriber.
func WithSubscriberBuffer[T any](buffer int) SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		subscriber.ch = make(chan message[T], buffer)
	}
}

// WithSubscriberErrorHandler sets the function called with every error handle returns, with the ctx passed to Subscribe.
// Without it, errors are discarded.
func WithSubscriberErrorHandler[T any](handler func(ctx context.Context, err error)) SubscriberOption[T] {
	return func(subscriber *subscriber[T]) {
		subscriber.errorHandler = handler
	}
}

// WithSubscriberDefaultMessageOptions sets message options applied to every message sent to the subscriber, before the
// options passed to Broadcast, which override them. With WithMessageAsync, for instance, no Broadcast waits for this
// subscriber, which may then receive the messages of successive Broadcasts out of order.
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

// WithMessageAsync makes Broadcast return right away instead of waiting for each subscriber to take the message: it is
// sent to every subscriber from its own goroutine, until the subscriber takes it, ctx is done or the message's timeout
// runs out. A slow subscriber then holds up neither Broadcast nor the other subscribers, but a subscriber may receive
// the messages of successive Broadcasts out of order.
func WithMessageAsync[T any]() MessageOptions[T] {
	return func(message *message[T]) {
		message.async = true
	}
}

// WithMessageErrorHandler sets a function called with every error about this message, on top of the error handler of
// the subscriber it failed on: a *HandleError when handle fails, with the ctx passed to Subscribe and from the
// subscriber's goroutine, so a slow handler holds the subscriber up; a *TimeoutError or a *SubscriberClosedError when
// Broadcast could not hand the message over, with the ctx passed to Broadcast. Every subscriber shares it, so it may be
// called concurrently, and after Broadcast has returned.
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
