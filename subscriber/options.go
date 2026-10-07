package subscriber

import (
	"context"
	"iter"
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

// WithBuffer sets the subscriber's buffer size: Broadcast waits for it only once the buffer is full.
func WithBuffer[T any](buffer int) Option[T] {
	return func(config *config[T]) {
		config.buffer = buffer
	}
}

// WithErrorHandler sets the handler for every error about the subscriber's messages: handle's, and why Broadcast could
// not hand one over. It runs where the error happens, in the subscriber's goroutine or Broadcast's, and holds that up.
// Without one, errors are discarded.
func WithErrorHandler[T any](handler func(ctx context.Context, err error)) Option[T] {
	return func(config *config[T]) {
		config.errorHandler = handler
	}
}

// WithDeadLetters puts every message the subscriber loses in store, before the error handlers run, so each message is
// handled or stored, once. Put runs where the error handlers do. If it fails, they get a *StoreError.
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

// WithDetachedContext keeps the subscriber once its ctx is done: it runs with context.WithoutCancel(ctx).
func WithDetachedContext[T any]() Option[T] {
	return func(config *config[T]) {
		config.detached = true
	}
}

// WithDefaultMessageOptions sets default message options, which a Broadcast's own override. A default of
// message.WithAsync brings its ctx caveat to every Broadcast.
func WithDefaultMessageOptions[T any](options ...message.Option[T]) Option[T] {
	return func(config *config[T]) {
		for _, option := range options {
			option(&config.defaults)
		}
	}
}

// WithUnsubscribeOptions sets default unsubscribe options, which Unsubscribe's own override. They are the only ones
// Close applies.
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

// WithEvictAfter unsubscribes the subscriber, with WithUnsubscribeDiscard, once it has lost n messages in a row to a
// *TimeoutError or *DroppedError, so that a stuck subscriber stops costing every Broadcast its timeout. A message taken
// resets the count. The evicting loss is reported as an *EvictedError, then passed to onEvict unless nil, once, where
// the error handlers run. n <= 0, the default, never evicts. Eviction is for good, not a way to reconnect.
func WithEvictAfter[T any](n int, onEvict func(ctx context.Context, evicted *EvictedError[T])) Option[T] {
	return func(config *config[T]) {
		config.evictAfter = n
		config.onEvict = onEvict
	}
}

// WithFilter skips every message keep rejects. keep runs in Broadcast's goroutine before anything else, so a skipped
// message costs a call rather than a wake-up, and concurrent Broadcasts may call it at once. A skipped message is
// neither reported nor counted, in Stats, WithEvictAfter or Broadcast's result. Several WithFilter run in order until
// one rejects. A nil keep is ignored. See package filter.
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

// WithAsyncLimit bounds the async sends (message.WithAsync) under way to the subscriber at n. Each holds a goroutine
// until taken or the Broadcast ctx ends, so a stuck subscriber piles them up. Past n, an async message is dropped with a
// *DroppedError, which counts towards WithEvictAfter. n <= 0, the default, means no limit.
func WithAsyncLimit[T any](n int) Option[T] {
	return func(config *config[T]) {
		config.asyncLimit = n
	}
}

// WithReplay makes the subscriber handle each value of replay before any broadcast message: what it lost before a
// restart, read back from its dead letters, or the current value for a late subscriber (slices.Values). Its goroutine
// replays, so Subscribe does not wait, but a Broadcast meanwhile waits as for a busy handle. Each value goes through
// the middlewares, error handlers and dead letters, and counts in Stats, like a message, but neither towards
// WithEvictAfter nor in Broadcast's result.
//
// yield returns once the value is handled or reported, so replay can ack it then. Once the subscriber discards, yield
// returns false without handling the value, and the rest stays in the source. Without discard, an unsubscribed
// subscriber keeps replaying, and Shutdown waits for it. A value lost again goes back to the dead letters, so a replay
// reading them should yield only what they held when it started.
func WithReplay[T any](replay iter.Seq[T]) Option[T] {
	return func(config *config[T]) {
		config.replay = replay
	}
}

// WithOnDone adds onDone, which runs once the subscriber is unsubscribed, whatever removed it, and done with what it
// took, so that it can release what handle used. It runs in the subscriber's goroutine after the last call to handle or the loop body,
// so they can share state without a lock. Shutdown waits for it. It never runs for a SubscribeSeq loop never ranged.
// Several run in order. A nil onDone is ignored.
func WithOnDone[T any](onDone func()) Option[T] {
	return func(config *config[T]) {
		if onDone == nil {
			return
		}
		previous := config.onDone
		if previous == nil {
			config.onDone = onDone

			return
		}
		config.onDone = func() {
			previous()
			onDone()
		}
	}
}

// WithUnsubscribeDiscard makes the subscriber report what it has left as *ClosedError instead of handling it, and ends
// a SubscribeSeq loop. A handle already running finishes. It has no effect on a subscriber already unsubscribed.
func WithUnsubscribeDiscard() UnsubscribeOption {
	return func(unsubscription *unsubscription) {
		unsubscription.discard = true
	}
}

// WithUnsubscribeDeliver makes the subscriber handle what it has left. It is the default, so it only overrides
// WithUnsubscribeOptions.
func WithUnsubscribeDeliver() UnsubscribeOption {
	return func(unsubscription *unsubscription) {
		unsubscription.discard = false
	}
}
