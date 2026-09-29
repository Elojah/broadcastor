package subscriber

import (
	"context"
	"time"

	"github.com/elojah/broadcastor/message"
)

// Option configures a subscriber, when passed to Subscribe or SubscribeSeq.
type Option[T any] func(config *config[T])

// UnsubscribeOption configures an Unsubscribe, or every one with WithUnsubscribeOptions.
type UnsubscribeOption func(unsubscription *unsubscription)

// unsubscription is what UnsubscribeOptions set.
type unsubscription struct {
	discard bool
}

// WithBuffer sets the size of the subscriber's channel buffer: Broadcast waits for the subscriber only once it is
// full.
func WithBuffer[T any](buffer int) Option[T] {
	return func(config *config[T]) {
		config.buffer = buffer
	}
}

// WithErrorHandler sets the handler for every error about the subscriber's messages: those handle returns, and why
// Broadcast could not hand one over. It runs where the error happens, in the subscriber's goroutine or in Broadcast's,
// so a slow one holds that up. Without one, errors are discarded.
func WithErrorHandler[T any](handler func(ctx context.Context, err error)) Option[T] {
	return func(config *config[T]) {
		config.errorHandler = handler
	}
}

// WithStore gives store every message the subscriber loses, right before the error handlers, so every message is
// either handled or stored, once. Put runs where the error handlers do, so a slow one holds up Broadcast too. If it
// fails, the error handlers get a *StoreError and the message is not stored again.
func WithStore[T any](store Store[T]) Option[T] {
	return func(config *config[T]) {
		config.store = store
	}
}

// WithFilter makes Broadcast skip the messages keep rejects: they are neither sent, nor counted, nor reported, so they
// never hold Broadcast up nor reach the store. keep runs in Broadcast's goroutine, and may run concurrently, so it must
// be quick and safe for concurrent use. Unlike WithMiddleware, it applies to SubscribeSeq too.
func WithFilter[T any](keep func(msg T) bool) Option[T] {
	return func(config *config[T]) {
		config.filter = keep
	}
}

// WithMiddleware appends middlewares around handle, the first one outermost. It has no effect on SubscribeSeq.
func WithMiddleware[T any](middlewares ...Middleware[T]) Option[T] {
	return func(config *config[T]) {
		config.middlewares = append(config.middlewares, middlewares...)
	}
}

// WithDetachedContext keeps the subscriber subscribed once its ctx is done. It runs with context.WithoutCancel(ctx)
// instead: the same values, never done.
func WithDetachedContext[T any]() Option[T] {
	return func(config *config[T]) {
		config.detached = true
	}
}

// WithDefaultMessageOptions sets message options for every message sent to the subscriber, which a Broadcast's own
// options override. A default of message.WithAsync has the same ctx caveat as message.WithAsync, for callers who may
// not know about it.
func WithDefaultMessageOptions[T any](options ...message.Option[T]) Option[T] {
	return func(config *config[T]) {
		for _, option := range options {
			option(&config.defaults)
		}
	}
}

// WithUnsubscribeOptions sets unsubscribe options for every Unsubscribe of the subscriber, which its own options
// override, and for Close, which has none.
func WithUnsubscribeOptions[T any](options ...UnsubscribeOption) Option[T] {
	return func(config *config[T]) {
		for _, option := range options {
			option(&config.unsubscribeDefaults)
		}
	}
}

// WithTimeout is WithDefaultMessageOptions(message.WithTimeout(timeout)).
func WithTimeout[T any](timeout time.Duration) Option[T] {
	return func(config *config[T]) {
		config.defaults.Timeout = timeout
	}
}

// WithUnsubscribeDiscard makes the subscriber report every message it takes once unsubscribed as a *ClosedError,
// instead of handling it, and ends a SubscribeSeq loop. Unsubscribe still does not wait, so handle may yet run for one
// message. It has no effect if the subscriber was already unsubscribed.
func WithUnsubscribeDiscard() UnsubscribeOption {
	return func(unsubscription *unsubscription) {
		unsubscription.discard = true
	}
}

// WithUnsubscribeDeliver makes the subscriber handle every message it takes once unsubscribed. It is the default, so
// it only overrides WithUnsubscribeOptions.
func WithUnsubscribeDeliver() UnsubscribeOption {
	return func(unsubscription *unsubscription) {
		unsubscription.discard = false
	}
}
