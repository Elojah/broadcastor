// Package subscriber holds the Subscriber a Broadcastor manages, the options passed to Subscribe, SubscribeSeq and
// Unsubscribe, Handler and Middleware, and the errors about a subscriber's messages.
package subscriber

import (
	"context"
	"iter"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/message"
)

// Subscriber is one subscription of a Broadcastor. Its channel is read either by Consume or by a single Seq loop.
type Subscriber[T any] struct {
	id     uuid.UUID
	ch     chan message.Message[T]
	config config[T]

	// ctx handles and reports every message that has no ctx of its own. Set by ContextLifetime.
	ctx context.Context //nolint:containedctx // the subscription's, which outlives every call

	// discarding makes Consume and pull report messages instead of processing them.
	discarding atomic.Bool

	// stopContextLifetime cancels the AfterFunc set by ContextLifetime. nil with WithDetachedContext.
	stopContextLifetime func() bool

	// refs counts the subscription plus each Broadcast sending on ch. Whoever drops it to 0 closes ch, so ch is never
	// closed under a sender.
	refs atomic.Int64
}

// config is what Options set.
type config[T any] struct {
	buffer              int
	defaults            message.Config
	unsubscribeDefaults unsubscription
	errorHandler        func(ctx context.Context, err error)
	store               Store[T]
	middlewares         []Middleware[T]
	detached            bool
}

// New returns a subscriber holding the subscription's reference, which Unsubscribe drops.
func New[T any](id uuid.UUID, options ...Option[T]) *Subscriber[T] {
	var config config[T]
	for _, option := range options {
		option(&config)
	}
	s := &Subscriber[T]{id: id, ch: make(chan message.Message[T], config.buffer), config: config}
	s.refs.Store(1)

	return s
}

// ID returns the subscriber's ID.
func (s *Subscriber[T]) ID() uuid.UUID {
	return s.id
}

// ContextLifetime makes the subscriber run with ctx and calls unsubscribe once ctx is done. With WithDetachedContext,
// it runs with context.WithoutCancel(ctx) instead and never calls unsubscribe. It returns the ctx the subscriber runs
// with, and must be called once, before anything else.
func (s *Subscriber[T]) ContextLifetime(ctx context.Context, unsubscribe func()) context.Context {
	if s.config.detached {
		s.ctx = context.WithoutCancel(ctx)

		return s.ctx
	}
	s.ctx = ctx
	s.stopContextLifetime = context.AfterFunc(ctx, unsubscribe)

	return ctx
}

// Deliver sends value to the subscriber and reports whether it took it, or, for an async message, whether a send was
// started. A parallel message returns false and a channel that yields the result instead. Failures are reported with
// the message's ctx, never ctx, which only bounds the wait.
func (s *Subscriber[T]) Deliver(ctx context.Context, value T, options ...message.Option[T]) (bool, <-chan bool) {
	m := message.New(value, s.config.defaults, options...)

	if !s.acquire() {
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: value}) //nolint:contextcheck // reported with the message's ctx, not the one bounding the wait

		return false, nil
	}

	if m.Config.Delivery == message.DeliveryAsync {
		go s.send(ctx, m)

		return true, nil
	}

	if m.Config.Delivery == message.DeliveryParallel {
		// Buffered, so the goroutine never waits for the reader.
		taken := make(chan bool, 1)
		go func() { taken <- s.send(ctx, m) }()

		return false, taken
	}

	return s.send(ctx, m), nil
}

// Unsubscribe drops the subscription's reference, with the unsubscribe defaults then options. Only whoever removed the
// subscriber from the Broadcastor calls it, once.
func (s *Subscriber[T]) Unsubscribe(options ...UnsubscribeOption) {
	u := s.config.unsubscribeDefaults
	for _, option := range options {
		option(&u)
	}
	// Before release, which may close ch right away: the reader must see it for every buffered message.
	if u.discard {
		s.discarding.Store(true)
	}
	// However the subscriber was removed, so nothing keeps waiting on a ctx that may never be done.
	if s.stopContextLifetime != nil {
		s.stopContextLifetime()
	}

	s.release()
}

// Consume calls handle, wrapped in the middlewares, for every message until ch is closed, and reports its errors.
func (s *Subscriber[T]) Consume(handle Handler[T]) {
	handle = chain(handle, s.config.middlewares...)
	for m := range s.ch {
		if s.discarding.Load() {
			s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value})

			continue
		}
		// Reported outside the chain, so Recover never catches a panic in an error handler.
		if err := handle(s.context(m), s.id, m.Value); err != nil {
			s.report(m, err)
		}
	}
}

// Seq returns SubscribeSeq's iterator, which can be ranged over once. When the loop ends, it calls unsubscribe and
// reports what is left on ch.
func (s *Subscriber[T]) Seq(unsubscribe func()) iter.Seq[T] {
	var ranged atomic.Bool

	return func(yield func(T) bool) {
		if ranged.Swap(true) {
			return
		}
		defer func() {
			unsubscribe()
			// In a goroutine: a Broadcast still sending here reports before it releases, and its error handler may be
			// waiting on this goroutine.
			go s.discard()
		}()

		s.pull(yield)
	}
}

// send hands m over and releases the caller's reference. It gives up once ctx is done or m's timeout runs out, or
// right away if m is non-blocking.
func (s *Subscriber[T]) send(ctx context.Context, m message.Message[T]) bool {
	defer s.release()

	if m.Config.Delivery == message.DeliveryNonBlocking {
		select {
		case s.ch <- m:
			return true
		default:
			s.report(m, &DroppedError[T]{SubscriberID: s.id, Message: m.Value}) //nolint:contextcheck // reported with the message's ctx, not the one bounding the wait

			return false
		}
	}

	sendCtx := ctx
	if m.Config.Timeout > 0 {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(ctx, m.Config.Timeout)
		defer cancel()
	}

	select {
	case s.ch <- m:
		return true
	case <-sendCtx.Done():
		s.report(m, &TimeoutError[T]{SubscriberID: s.id, Message: m.Value, Err: sendCtx.Err()}) //nolint:contextcheck // reported with the message's ctx, not the one bounding the wait

		return false
	}
}

// acquire takes a reference for a Broadcast, unless ch is already closed.
func (s *Subscriber[T]) acquire() bool {
	for {
		n := s.refs.Load()
		if n == 0 {
			return false
		}
		if s.refs.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// release drops a reference and closes ch if it was the last one.
func (s *Subscriber[T]) release() {
	if s.refs.Add(-1) == 0 {
		close(s.ch)
	}
}

// context returns m's own ctx, or else the subscriber's.
func (s *Subscriber[T]) context(m message.Message[T]) context.Context {
	if m.Config.Context != nil {
		return m.Config.Context
	}

	return s.ctx
}

// report stores m, then passes err to the subscriber's error handler, then to m's. A failed Put turns err into a
// *StoreError.
func (s *Subscriber[T]) report(m message.Message[T], err error) {
	ctx := s.context(m)
	if s.config.store != nil {
		// Never done: ctx being done is often why m was lost.
		if putErr := s.config.store.Put(context.WithoutCancel(ctx), Record[T]{SubscriberID: s.id, Message: m.Value, Err: err}); putErr != nil {
			err = &StoreError[T]{SubscriberID: s.id, Message: m.Value, Err: putErr, Cause: err}
		}
	}
	if s.config.errorHandler != nil {
		s.config.errorHandler(ctx, err)
	}
	if m.Config.ErrorHandler != nil {
		m.Config.ErrorHandler(ctx, err)
	}
}

// pull is Consume for Seq: it yields messages until yield returns false, ctx is done, ch is closed or the subscriber
// is discarding.
func (s *Subscriber[T]) pull(yield func(T) bool) {
	for s.ctx.Err() == nil && !s.discarding.Load() {
		select {
		case m, ok := <-s.ch:
			if !ok {
				return
			}
			if s.discarding.Load() {
				s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value})

				return
			}
			if !yield(m.Value) {
				return
			}
		case <-s.ctx.Done():
			return
		}
	}
}

// discard reports every message left on ch until it is closed, so no Broadcast waits on a loop that has ended.
func (s *Subscriber[T]) discard() {
	for m := range s.ch {
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value})
	}
}
