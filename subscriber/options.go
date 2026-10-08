package subscriber

import (
	"context"
	"iter"
	"time"

	"github.com/google/uuid"

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

// WithEvict unsubscribes the subscriber, with WithUnsubscribeDiscard, on the first error evict returns true for: a
// *TimeoutError or *DroppedError, in Broadcast's goroutine, so that a stuck subscriber stops costing every Broadcast
// its timeout, or handle's error past every middleware, in the subscriber's, such as one meaning it cannot go on. So
// evict may run concurrently. It never gets a *ClosedError. The evicting error is reported as an *EvictedError, then
// sent on evicted unless nil, once, before the subscriber's WithDone. Like signal.Notify, the send never blocks: give
// evicted room for one per subscriber sending on it, or only the error handlers get it. To evict after several errors,
// count them in evict, or in a middleware returning an error evict matches. A nil evict, the default, never evicts.
// Eviction is for good, not a way to reconnect.
func WithEvict[T any](evict func(err error) bool, evicted chan<- *EvictedError[T]) Option[T] {
	return func(config *config[T]) {
		config.evict = evict
		config.evicted = evicted
	}
}

// WithFilter skips every message keep rejects. keep runs in Broadcast's goroutine before anything else, so a skipped
// message costs a call rather than a wake-up, and concurrent Broadcasts may call it at once. A skipped message is
// neither reported, nor passed to WithEvict's evict, nor counted, in Stats or Broadcast's result. Several WithFilter
// run in order until one rejects. A nil keep is ignored. See package filter.
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
// *DroppedError, which WithEvict's evict gets like any other. n <= 0, the default, means no limit.
func WithAsyncLimit[T any](n int) Option[T] {
	return func(config *config[T]) {
		config.asyncLimit = n
	}
}

// WithReplay makes the subscriber handle each value of replay before any broadcast message: what it lost before a
// restart, read back from its dead letters, or the current value for a late subscriber (slices.Values). Its goroutine
// replays, so Subscribe does not wait, but a Broadcast meanwhile waits as for a busy handle. Each value goes through
// the middlewares, error handlers, WithEvict and dead letters, and counts in Stats, like a message, but not in
// Broadcast's result.
//
// yield returns once the value is handled or reported, so replay can ack it then. Once the subscriber discards, evicted
// included, yield returns false without handling the value, and the rest stays in the source. Without discard, an
// unsubscribed subscriber keeps replaying, and Shutdown waits for it. A value lost again goes back to the dead letters,
// so a replay reading them should yield only what they held when it started.
func WithReplay[T any](replay iter.Seq[T]) Option[T] {
	return func(config *config[T]) {
		config.replay = replay
	}
}

// WithDone sends the subscriber's ID on done once it is unsubscribed, whatever removed it, and done with what it took:
// after the last call to handle or the loop body, so that the receiver can release what handle used without a lock.
// Shutdown returns once it is sent, without waiting for the receiver: release after Shutdown. Like signal.Notify, the
// send never blocks: give done room for one per subscriber sending on it, or the ID is dropped. It is never sent for a
// SubscribeSeq loop never ranged. Several add up. A nil done is ignored.
func WithDone[T any](done chan<- uuid.UUID) Option[T] {
	return func(config *config[T]) {
		if done != nil {
			config.done = append(config.done, done)
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
