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

// WithEvictAfter unsubscribes the subscriber once it has lost n messages in a row as a *TimeoutError or a
// *DroppedError, so that a stuck subscriber stops costing every Broadcast its timeout. A message it takes starts the
// count again. It is unsubscribed with WithUnsubscribeDiscard, whatever its defaults, and the loss that evicts it is
// reported as an *EvictedError instead. 0 or less means never, the default.
func WithEvictAfter[T any](n int) Option[T] {
	return func(config *config[T]) {
		config.evictAfter = n
	}
}

// WithOrder makes the subscriber handle its messages in policy's order instead of the order it takes them in, which
// suits messages that carry their own time or sequence number. It holds each message for up to policy.Window, for one
// that sorts before it to arrive, and keeps taking messages meanwhile, so holding one does not hold Broadcast up. It
// takes none while one is due, so a slow handle still does. A message that sorts before one already handled is late:
// it is reported as a *LateError instead. Once its channel is closed, after Unsubscribe, the subscriber handles
// everything it holds right away, in order, or reports it with WithUnsubscribeDiscard. It applies to SubscribeSeq too.
func WithOrder[T any](policy OrderPolicy[T]) Option[T] {
	return func(config *config[T]) {
		config.order = &policy
	}
}

// WithReplay makes a new subscriber handle first the messages its Broadcastor's History holds (broadcastor.WithHistory)
// that keep accepts, oldest first, or merged in order with the live ones with WithOrder. It gets each message once,
// either from the history or live, with none missed in between. nil keeps every one. Subscribe reads the history once
// it has subscribed the subscriber, after waiting for the Appends under way, and calls keep, so a slow History.Read
// holds Subscribe up. A replayed message gets the subscriber's ctx and its default message options, not those its
// Broadcast was given. Without a history, it has no effect.
func WithReplay[T any](keep func(msg T) bool) Option[T] {
	return func(config *config[T]) {
		if keep == nil {
			keep = func(T) bool { return true }
		}
		config.replay = keep
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
