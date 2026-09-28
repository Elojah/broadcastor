package subscriber

import (
	"context"
	"time"

	"github.com/elojah/broadcastor/message"
)

// Option configures a subscriber when it is passed to Subscribe or SubscribeSeq.
type Option[T any] func(config *config[T])

// UnsubscribeOption configures what a subscriber does with the messages it takes once it is unsubscribed, when passed
// to Unsubscribe or to WithDefaultUnsubscribeOptions.
type UnsubscribeOption func(unsubscription *unsubscription)

// unsubscription is how a single Unsubscribe or Close removes one subscriber, as set by the subscriber's default
// unsubscribe options and then the Unsubscribe's own.
type unsubscription struct {
	// discard makes the subscriber report the messages it takes from then on instead of processing them.
	discard bool
}

// WithBuffer sets the channel buffer size for the subscriber.
func WithBuffer[T any](buffer int) Option[T] {
	return func(config *config[T]) {
		config.buffer = buffer
	}
}

// WithErrorHandler sets the function called with every error handle returns, as is, from the subscriber's goroutine
// and with the ctx passed to Subscribe. middleware.WrapError makes those errors a *HandleError, which tells which
// subscriber and message they are about. Without an error handler, errors are discarded.
func WithErrorHandler[T any](handler func(ctx context.Context, err error)) Option[T] {
	return func(config *config[T]) {
		config.errorHandler = handler
	}
}

// WithMiddleware wraps handle in middlewares, the first one outermost: it is called with each message, and calls the
// next one or not. Several of these options add up, in order. Middlewares run in the subscriber's goroutine, with the
// ctx passed to Subscribe, so a slow one holds the subscriber up like a slow handle. Whatever error the outermost one
// returns reaches the error handlers as is, and a panic in one crashes the program like a panic in handle, unless an
// outer middleware recovers it. Package middleware holds ready-made ones: middleware.Recover and middleware.WrapError,
// in that order, go first, then middleware.Retry. It has no effect on SubscribeSeq, whose loop body runs in the
// caller's goroutine.
func WithMiddleware[T any](middlewares ...Middleware[T]) Option[T] {
	return func(config *config[T]) {
		config.middlewares = append(config.middlewares, middlewares...)
	}
}

// WithAutoUnsubscribe unsubscribes the subscriber once the ctx passed to Subscribe or SubscribeSeq is done, like an
// Unsubscribe with no options, so its WithDefaultUnsubscribeOptions apply. Without it, cancelling that ctx leaves a
// Subscribe subscriber subscribed, and ends a SubscribeSeq loop only once the loop checks it.
//
// The subscriber is unsubscribed from a goroutine of its own as soon as ctx is done, or right after it is added if ctx
// already is, even while handle or the loop body is running, or before the loop has started: no later Broadcast waits
// for it. As after any Unsubscribe, it may still get messages from its buffer or from a Broadcast that was already
// sending to it, and processes them with the done ctx, unless it discards them. Once the subscriber is unsubscribed
// another way, nothing waits for ctx any more, so ctx may be one that is never done.
func WithAutoUnsubscribe[T any]() Option[T] {
	return func(config *config[T]) {
		config.autoUnsubscribe = true
	}
}

// WithDefaultMessageOptions sets message options applied to every message sent to the subscriber, before the options
// passed to Broadcast, which override them. With message.WithAsync, for instance, no Broadcast waits for this
// subscriber, which may then receive the messages of successive Broadcasts out of order, unless a Broadcast passes
// message.WithSync.
func WithDefaultMessageOptions[T any](options ...message.Option[T]) Option[T] {
	return func(config *config[T]) {
		for _, option := range options {
			option(&config.defaults)
		}
	}
}

// WithDefaultUnsubscribeOptions sets unsubscribe options applied whenever the subscriber is unsubscribed, before the
// options passed to Unsubscribe, which override them. They are the only ones Close applies. With
// WithUnsubscribeDiscard, for instance, the subscriber stops processing messages as soon as it is unsubscribed, by
// Unsubscribe or by Close, unless an Unsubscribe passes WithUnsubscribeDeliver.
func WithDefaultUnsubscribeOptions[T any](options ...UnsubscribeOption) Option[T] {
	return func(config *config[T]) {
		for _, option := range options {
			option(&config.unsubscribeDefaults)
		}
	}
}

// WithTimeout bounds how long every Broadcast waits for the subscriber to take its message, like message.WithTimeout.
// It is the same as WithDefaultMessageOptions(message.WithTimeout(timeout)), so a Broadcast passing message.WithTimeout
// overrides it, whether longer, shorter or 0 for none.
func WithTimeout[T any](timeout time.Duration) Option[T] {
	return func(config *config[T]) {
		config.defaults.Timeout = timeout
	}
}

// WithUnsubscribeDiscard makes the subscriber stop processing messages once it is unsubscribed. Every message it takes
// from then on, from its buffer or from a Broadcast that was already sending to it, is reported to its error handlers
// as a *ClosedError instead of being passed to handle, with the ctx passed to Subscribe. A SubscribeSeq loop ends
// instead of yielding it, and reports it with the ctx passed to SubscribeSeq. Unsubscribe still does not wait: handle,
// or the loop body, may be running or about to start for one message when it returns, but for no other one. It has no
// effect when the subscriber was already unsubscribed, by another Unsubscribe or by Close, whose options applied
// instead. WithUnsubscribeDiscard and WithUnsubscribeDeliver exclude each other: the last one given wins.
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
