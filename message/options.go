package message

import (
	"context"
	"time"
)

// Option configures how a Broadcast hands its message to each subscriber, when passed to Broadcast or to
// subscriber.WithDefaultMessageOptions.
type Option[T any] func(config *Config)

// WithSync makes Broadcast wait for each subscriber in turn to take the message, until ctx is done or the message's
// timeout runs out. It is the default, so it is only needed to override a subscriber's default of WithParallel,
// WithAsync or WithNonBlocking for one Broadcast. WithSync, WithParallel, WithAsync and WithNonBlocking exclude each
// other: the last one given wins.
func WithSync[T any]() Option[T] {
	return func(config *Config) {
		config.Delivery = DeliverySync
	}
}

// WithParallel makes Broadcast send the message to every subscriber at once, each from its own goroutine, and return
// once each has taken it or missed it, when ctx is done or the message's timeout runs out. A slow subscriber then
// holds up nobody else, but still holds up Broadcast, and a subscriber gets the messages of successive Broadcasts from
// one goroutine in order. ctx and the timeout apply to every subscriber from the same moment, so Broadcast takes at
// most the timeout, not the timeout for each subscriber, and every subscriber that misses the message has its error
// handlers told before Broadcast returns, possibly concurrently. A subscriber that WithSync applies to in the same
// Broadcast is still waited for in turn, and holds up every send Broadcast starts after it.
func WithParallel[T any]() Option[T] {
	return func(config *Config) {
		config.Delivery = DeliveryParallel
	}
}

// WithAsync makes Broadcast return right away instead of waiting for each subscriber to take the message: it is sent
// to every subscriber from its own goroutine, until the subscriber takes it, ctx is done or the message's timeout runs
// out. A slow subscriber then holds up neither Broadcast nor the other subscribers, but a subscriber may receive the
// messages of successive Broadcasts out of order.
//
// Those goroutines keep using ctx after Broadcast has returned, having already been counted. So a ctx that is done once
// the caller returns, such as a request's or one whose cancel is deferred, makes every subscriber that has not taken
// the message by then miss it. To let the sends outlive the caller, pass Broadcast context.WithoutCancel(ctx) and bound
// them with WithTimeout instead, and keep ctx's values for handle with WithContext:
//
//	detached := context.WithoutCancel(ctx)
//	b.Broadcast(detached, msg, message.WithAsync[T](), message.WithTimeout[T](time.Second), message.WithContext[T](detached))
func WithAsync[T any]() Option[T] {
	return func(config *Config) {
		config.Delivery = DeliveryAsync
	}
}

// WithNonBlocking makes Broadcast hand the message only to the subscribers that can take it right away, and never
// wait: an unbuffered subscriber must be idle, waiting for its next message, and a buffered one must have room left in
// its buffer. Every other subscriber misses the message, and its error handlers are given a *subscriber.DroppedError.
// Since Broadcast never waits, neither the ctx passed to it nor the message's timeout plays any part. A slow subscriber
// then holds up nobody, and still gets the messages it does take in order.
func WithNonBlocking[T any]() Option[T] {
	return func(config *Config) {
		config.Delivery = DeliveryNonBlocking
	}
}

// WithContext makes every subscriber handle the message with ctx instead of the ctx passed to Subscribe or
// SubscribeSeq: handle and its middlewares are called with ctx, and the error handlers are given every error about the
// message with ctx, including when Broadcast could not hand it over. So ctx's values, such as a trace or request ID,
// reach handle, which those of the ctx passed to Broadcast never do: that one only bounds how long Broadcast waits for
// each subscriber to take the message.
//
// ctx is used as is, so a subscriber that takes the message once ctx is done, from its buffer or from an async send,
// handles it with a done ctx, and middleware.Retry gives up on it. Pass context.WithoutCancel(ctx) to keep only its
// values. The subscriber's own ctx plays no part in handling the message: neither its values nor its cancellation reach
// handle, although it being done still unsubscribes the subscriber. A SubscribeSeq loop body is given no ctx, so there
// ctx only reaches the error handlers. A nil ctx means none, which overrides a default set with
// subscriber.WithDefaultMessageOptions.
func WithContext[T any](ctx context.Context) Option[T] {
	return func(config *Config) {
		config.Context = ctx
	}
}

// WithErrorHandler sets a function called with every error about this message, on top of the error handler of the
// subscriber it failed on: the error handle or its middlewares returned, such as a *subscriber.HandleError from
// middleware.WrapError or a *subscriber.PanicError from middleware.Recover, from the subscriber's goroutine, so a slow
// handler holds the subscriber up; a *subscriber.TimeoutError, a *subscriber.DroppedError or a *subscriber.ClosedError
// when Broadcast could not hand the message over; a *subscriber.ClosedError when a SubscribeSeq loop ended before
// yielding it, or when the subscriber took it after being unsubscribed with subscriber.WithUnsubscribeDiscard. Both
// handlers get every error with the ctx the message is handled with: the one WithContext gave it, or else the one the
// subscriber runs with, passed to Subscribe or SubscribeSeq, never the one passed to Broadcast. Every subscriber shares
// it, so it may be called concurrently, and after Broadcast has returned.
func WithErrorHandler[T any](handler func(ctx context.Context, err error)) Option[T] {
	return func(config *Config) {
		config.ErrorHandler = handler
	}
}

// WithTimeout bounds how long Broadcast waits for each subscriber to take the message, on top of ctx. Once it runs out,
// that subscriber misses the message, and its error handlers are given a *subscriber.TimeoutError wrapping
// context.DeadlineExceeded. Unlike a ctx deadline, which a synchronous Broadcast uses up across all subscribers, each
// subscriber gets the whole timeout, counted from when Broadcast gets to it: a synchronous Broadcast can then take up to
// the timeout for each subscriber, while a parallel one (WithParallel) gets to them all at once, and takes up to the
// timeout overall. A timeout of 0 or less means none, which overrides subscriber.WithTimeout.
func WithTimeout[T any](timeout time.Duration) Option[T] {
	return func(config *Config) {
		config.Timeout = timeout
	}
}
