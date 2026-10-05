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
// Broadcast but no other subscriber, and messages from one goroutine stay in order. ctx and the timeout start at the
// same moment for every subscriber.
func WithParallel[T any]() Option[T] {
	return func(config *Config) {
		config.Delivery = DeliveryParallel
		config.set |= fieldDelivery
	}
}

// WithAsync makes Broadcast send to every subscriber from its own goroutine and return right away. A slow subscriber
// holds up nobody, but successive messages may arrive out of order. Each send holds a goroutine until the subscriber
// takes the message, which subscriber.WithAsyncLimit bounds.
//
// The sends keep using ctx after Broadcast returns, so cancelling it, as a request's deferred cancel does, drops every
// message not taken yet. To let the sends outlive the caller:
//
//	detached := context.WithoutCancel(ctx)
//	b.Broadcast(detached, msg, message.WithAsync[T](), message.WithTimeout[T](time.Second), message.WithContext[T](detached))
func WithAsync[T any]() Option[T] {
	return func(config *Config) {
		config.Delivery = DeliveryAsync
		config.set |= fieldDelivery
	}
}

// WithNonBlocking makes Broadcast never wait: a subscriber that is busy or has a full buffer misses the message, with a
// *subscriber.DroppedError. ctx and the timeout play no part.
func WithNonBlocking[T any]() Option[T] {
	return func(config *Config) {
		config.Delivery = DeliveryNonBlocking
		config.set |= fieldDelivery
	}
}

// WithContext makes handle, its middlewares and the error handlers get ctx for this message, instead of the
// subscription's. ctx is used as is, so a message taken once ctx is done is handled with a done ctx: pass
// context.WithoutCancel(ctx) to keep only its values. nil overrides a default.
func WithContext[T any](ctx context.Context) Option[T] {
	return func(config *Config) {
		config.Context = ctx
		config.set |= fieldContext
	}
}

// WithErrorHandler adds a handler for every error about this message, called after the subscriber's own. Every
// subscriber shares it, so it may be called concurrently and after Broadcast returns.
func WithErrorHandler[T any](handler func(ctx context.Context, err error)) Option[T] {
	return func(config *Config) {
		config.ErrorHandler = handler
		config.set |= fieldErrorHandler
	}
}

// WithTimeout bounds how long Broadcast waits for each subscriber, counted from when Broadcast gets to it, whereas a ctx
// deadline is shared by all of them. A subscriber that misses it gets a *subscriber.TimeoutError. 0 or less means
// none, which overrides subscriber.WithTimeout.
func WithTimeout[T any](timeout time.Duration) Option[T] {
	return func(config *Config) {
		config.Timeout = timeout
		config.set |= fieldTimeout
	}
}
