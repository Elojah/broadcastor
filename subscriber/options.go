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

// WithDefaultMessageOptions sets default message options, which a Broadcast's own override.
func WithDefaultMessageOptions[T any](options ...message.Option[T]) Option[T] {
	return func(config *config[T]) {
		for _, option := range options {
			option(&config.defaults)
		}
	}
}

// WithUnsubscribeOptions sets default unsubscribe options, which Unsubscribe's own override. Close and a done ctx
// apply only these.
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
// *TimeoutError or *DroppedError, from Broadcast's goroutine, or handle's error past every middleware, from the
// subscriber's. evict may run concurrently, and never gets a *ClosedError. A nil evict never evicts. The error handlers
// get the evicting error as an *EvictedError.
func WithEvict[T any](evict func(err error) bool) Option[T] {
	return func(config *config[T]) {
		config.evict = evict
	}
}

// WithFilter skips every message keep rejects, before anything else, so the subscriber never wakes up for it, and it
// is neither reported nor counted. keep runs in Broadcast's goroutine, so it may run concurrently. Several WithFilter
// run in order until one rejects. A nil keep is ignored.
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

// WithAsyncLimit bounds the async sends (message.WithAsync) under way to the subscriber at n: past n, an async message
// is dropped with a *DroppedError. n <= 0, the default, means no limit.
func WithAsyncLimit[T any](n int) Option[T] {
	return func(config *config[T]) {
		config.asyncLimit = n
	}
}

// WithReplay makes the subscriber handle each value of replay, such as its dead letters from before a restart, before
// any message. Each value is handled, reported and counted like a message, from the subscriber's goroutine, and a
// Broadcast meanwhile waits as for a busy handle.
//
// yield returns once the value is handled or reported, so replay can ack it then, and returns false once the
// subscriber discards. A value lost again goes back to the dead letters, so a replay of them should yield only what
// they held when it started.
func WithReplay[T any](replay iter.Seq[T]) Option[T] {
	return func(config *config[T]) {
		config.replay = replay
	}
}

// WithDone sends the subscriber's ID on done once it is unsubscribed, whatever removed it, and done with what it took:
// after the last call to handle or the loop body, and before Shutdown returns. Like signal.Notify, the send never
// blocks: give done room for one per subscriber sharing it. It is never sent for a SubscribeSeq loop never ranged.
// Several add up, and a nil done is ignored.
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
