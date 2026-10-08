package message

import (
	"context"
	"time"
)

// Option configures a message, when passed to Broadcast or to subscriber.WithDefaultMessageOptions.
type Option[T any] func(config *Config)

// WithSync makes Broadcast wait for each subscriber in turn. It is the default, so it only overrides a subscriber's
// default delivery.
func WithSync[T any]() Option[T] {
	return func(config *Config) {
		config.Delivery = DeliverySync
		config.set |= fieldDelivery
	}
}

// WithParallel makes Broadcast send to every subscriber at once, then wait for all of them. A slow subscriber holds up
// Broadcast but no other subscriber. Messages from one goroutine stay in order.
func WithParallel[T any]() Option[T] {
	return func(config *Config) {
		config.Delivery = DeliveryParallel
		config.set |= fieldDelivery
	}
}

// WithAsync makes Broadcast send to each subscriber from a goroutine of its own and return at once. Messages may arrive
// out of order, and each send holds a goroutine until taken (see subscriber.WithAsyncLimit).
//
// The sends keep using the Broadcast ctx, so cancelling it drops every message not taken yet. To outlive the caller:
//
//	detached := context.WithoutCancel(ctx)
//	b.Broadcast(detached, msg, message.WithAsync[T](), message.WithTimeout[T](time.Second), message.WithContext[T](detached))
func WithAsync[T any]() Option[T] {
	return func(config *Config) {
		config.Delivery = DeliveryAsync
		config.set |= fieldDelivery
	}
}

// WithNonBlocking makes Broadcast never wait: a busy subscriber, or one with a full buffer, misses the message with a
// *subscriber.DroppedError.
func WithNonBlocking[T any]() Option[T] {
	return func(config *Config) {
		config.Delivery = DeliveryNonBlocking
		config.set |= fieldDelivery
	}
}

// WithContext gives handle, its middlewares and the error handlers ctx for this message, instead of the subscription's.
// It is used as is, done or not: pass context.WithoutCancel(ctx) to keep only its values. nil overrides a default.
func WithContext[T any](ctx context.Context) Option[T] {
	return func(config *Config) {
		config.Context = ctx
		config.set |= fieldContext
	}
}

// WithErrorHandler adds a handler for every error about this message, after the subscriber's own. Every subscriber
// shares it, so it may run concurrently, and after Broadcast returns.
func WithErrorHandler[T any](handler func(ctx context.Context, err error)) Option[T] {
	return func(config *Config) {
		config.ErrorHandler = handler
		config.set |= fieldErrorHandler
	}
}

// WithTimeout bounds the wait for each subscriber, from when Broadcast gets to it, whereas a ctx deadline is shared.
// A subscriber that misses it gets a *subscriber.TimeoutError. 0 or less means none, overriding subscriber.WithTimeout.
func WithTimeout[T any](timeout time.Duration) Option[T] {
	return func(config *Config) {
		config.Timeout = timeout
		config.set |= fieldTimeout
	}
}
