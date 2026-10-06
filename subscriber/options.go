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

// WithDeadLetters gives store every message the subscriber loses, right before the error handlers, so every message
// is either handled or stored, once. Put runs where the error handlers do, so a slow one holds up Broadcast too. If it
// fails, the error handlers get a *StoreError and the message is not stored again.
func WithDeadLetters[T any](store Store[T]) Option[T] {
	return func(config *config[T]) {
		config.store = store
	}
}

// WithMiddleware appends middlewares around handle, or a SubscribeSeq loop body, the first one outermost.
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
// options override. A default of message.WithAsync brings its ctx caveat to every Broadcast, even one whose caller
// did not ask for async.
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

// WithEvictAfter unsubscribes the subscriber once it has lost n messages in a row as a *TimeoutError or a
// *DroppedError, so that a stuck subscriber stops costing every Broadcast its timeout. A message it takes starts the
// count again. It is unsubscribed with WithUnsubscribeDiscard, whatever its defaults, and the loss that evicts it is
// reported as an *EvictedError instead. Once it is reported, onEvict gets that *EvictedError with the ctx the error
// handlers got: once per subscriber, where the error handlers run, so a slow one holds up Broadcast too. onEvict may be
// nil. n at 0 or less means never, the default.
func WithEvictAfter[T any](n int, onEvict func(ctx context.Context, evicted *EvictedError[T])) Option[T] {
	return func(config *config[T]) {
		config.evictAfter = n
		config.onEvict = onEvict
	}
}

// WithFilter makes the subscriber skip every message keep returns false for. keep runs in Broadcast's goroutine before
// anything else, so a skipped message costs a call instead of waking the subscriber up, and concurrent Broadcasts may
// call it at once. A skipped message is neither reported nor counted: not in Stats, nor towards WithEvictAfter, nor in
// Broadcast's return value. Each WithFilter adds a filter, called in order until one rejects the message, so a filter
// sees only the messages those before it kept. A nil keep keeps every message. Package filter holds filters for
// readings that repeat themselves.
func WithFilter[T any](keep func(msg T) bool) Option[T] {
	return func(config *config[T]) {
		if keep == nil {
			return
		}
		previous := config.filter
		if previous == nil {
			config.filter = keep

			return
		}
		config.filter = func(msg T) bool { return previous(msg) && keep(msg) }
	}
}

// WithAsyncLimit bounds the async sends (message.WithAsync) under way to the subscriber at n. Each one holds a
// goroutine and its message until the subscriber takes it or the Broadcast ctx ends, so without a timeout a stuck
// subscriber piles them up. Past n, an async message is dropped right away with a *DroppedError, which counts towards
// WithEvictAfter. 0 or less means no limit, the default: a limit drops messages silently without an error handler,
// whereas Stats.Sending shows a pile-up.
func WithAsyncLimit[T any](n int) Option[T] {
	return func(config *config[T]) {
		config.asyncLimit = n
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
