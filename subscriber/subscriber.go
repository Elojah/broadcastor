// Package subscriber holds what a broadcastor.Broadcastor subscriber is made of: the options passed to Subscribe,
// SubscribeSeq and Unsubscribe, the Handler that Subscribe calls and the Middleware that wraps it, the errors its error
// handlers are given, and the Subscriber itself. Subscribe, SubscribeSeq, Unsubscribe, Close and Broadcast are the
// Broadcastor's methods.
package subscriber

import (
	"context"
	"iter"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/message"
)

// Subscriber is what a Broadcastor adds for every Subscribe or SubscribeSeq, and hands the message of every Broadcast
// to. Only the Broadcastor needs one: it never hands out the subscribers it adds. Its channel is read either by Consume
// or by ranging over the iterator Seq returns, once.
type Subscriber[T any] struct {
	id     uuid.UUID
	ch     chan message.Message[T]
	config config[T]

	// ctx is the ctx the subscriber runs with, set by ContextLifetime: every message without a ctx of its own is handled
	// and reported with it, and a Seq loop ends once it is done.
	ctx context.Context //nolint:containedctx // the subscription's, which outlives every call

	// discarding is set when the subscriber is unsubscribed with WithUnsubscribeDiscard, before ch is released: from then
	// on, Consume and pull report every message they take instead of processing it.
	discarding atomic.Bool

	// stopContextLifetime undoes the context.AfterFunc that ContextLifetime set up, once the subscriber was removed
	// another way. It is nil with WithDetachedContext.
	stopContextLifetime func() bool

	// refs is 1 for the subscription itself, plus 1 for each Broadcast currently sending on ch. Whoever drops it to 0
	// closes ch, so ch is never closed while a Broadcast can still send on it.
	refs atomic.Int64
}

// config is what the options passed to Subscribe or SubscribeSeq set, which New then builds the subscriber from.
// Options change nothing else.
type config[T any] struct {
	// buffer is the size of the subscriber's channel buffer, 0 for unbuffered.
	buffer int

	// defaults is what every message sent to this subscriber starts from, before the Broadcast's own options.
	defaults message.Config

	// unsubscribeDefaults is what every Unsubscribe and Close of this subscriber starts from, before the Unsubscribe's
	// own options.
	unsubscribeDefaults unsubscription

	// errorHandler is called with every error handle returns, or nil to discard them.
	errorHandler func(ctx context.Context, err error)

	// store is given every message the subscriber loses, or nil for none.
	store Store[T]

	// middlewares wrap handle, the first one outermost.
	middlewares []Middleware[T]

	// detached keeps the subscriber subscribed once the ctx passed to Subscribe or SubscribeSeq is done, and makes it run
	// with context.WithoutCancel of that ctx.
	detached bool
}

// New returns a subscriber with the given ID, set up by options. It holds the subscription's reference, which
// Unsubscribe drops, and nothing reads its channel yet.
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

// ContextLifetime ties the subscription to ctx: it calls unsubscribe once ctx is done, and makes ctx itself the one the
// subscriber runs with, which it returns. With WithDetachedContext, it calls nothing, and the subscriber runs with
// context.WithoutCancel(ctx), which is never done. Unsubscribe undoes it, and Deliver, Consume and Seq need that ctx, so
// it must be called before anything else, and once. When ctx is already done, unsubscribe is called right away, from a
// goroutine of its own.
func (s *Subscriber[T]) ContextLifetime(ctx context.Context, unsubscribe func()) context.Context {
	if s.config.detached {
		s.ctx = context.WithoutCancel(ctx)

		return s.ctx
	}
	s.ctx = ctx
	s.stopContextLifetime = context.AfterFunc(ctx, unsubscribe)

	return ctx
}

// Deliver hands value to the subscriber, as set up by its default message options and then options, and reports
// whether it took it, or whether a send was started for an async message. A parallel message is sent from a goroutine
// of its own, and Deliver returns right away with a channel that yields whether the subscriber took it once the send is
// done, and nil otherwise. Once ctx is done, the message's timeout runs out, or right away for a non-blocking message
// the subscriber cannot take, it gives up, and the subscriber's error handlers are given a *TimeoutError or a
// *DroppedError. If the subscriber was unsubscribed and its channel closed since the caller picked it up, they are
// given a *ClosedError instead. ctx only bounds the wait: the error handlers get the message's own ctx, or the
// subscriber's.
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
		// Buffered, so that the goroutine ends without waiting for the caller to read it.
		taken := make(chan bool, 1)
		go func() { taken <- s.send(ctx, m) }()

		return false, taken
	}

	return s.send(ctx, m), nil
}

// Unsubscribe drops the subscription's reference, and first makes the subscriber discard if its unsubscribe defaults,
// then options, say so. It must be called once, by whoever took the subscriber out of the Broadcastor.
func (s *Subscriber[T]) Unsubscribe(options ...UnsubscribeOption) {
	u := s.config.unsubscribeDefaults
	for _, option := range options {
		option(&u)
	}
	// Before release, which may close ch right away, so that the subscriber discards everything left in its buffer.
	if u.discard {
		s.discarding.Store(true)
	}
	// However it was removed, so that nothing keeps waiting for a ctx that may never be done.
	if s.stopContextLifetime != nil {
		s.stopContextLifetime()
	}

	// Closes the channel now, or once the last Broadcast still sending on it is done.
	s.release()
}

// Consume calls handle, wrapped in the subscriber's middlewares, with the message's ctx, or else the subscriber's, and
// the subscriber's ID for every message the subscriber takes, and reports the errors it returns, until the channel is
// closed. It is what the goroutine Subscribe starts runs.
func (s *Subscriber[T]) Consume(handle Handler[T]) {
	handle = chain(handle, s.config.middlewares...)
	for m := range s.ch {
		if s.discarding.Load() {
			s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value})

			continue
		}
		// Reported here rather than from a middleware, so that a panic in an error handler is not recovered.
		if err := handle(s.context(m), s.id, m.Value); err != nil {
			s.report(m, err)
		}
	}
}

// Seq returns the iterator SubscribeSeq returns, over the messages the subscriber takes, which ends once the
// subscriber's ctx is done. Once the loop ends, it calls unsubscribe, and reports every message the subscriber took but
// did not yield, from a goroutine of its own. It can be ranged over only once: any other range yields nothing.
func (s *Subscriber[T]) Seq(unsubscribe func()) iter.Seq[T] {
	var ranged atomic.Bool

	return func(yield func(T) bool) {
		if ranged.Swap(true) {
			return
		}
		defer func() {
			// Unless it already was, which is one way for the loop to end.
			unsubscribe()
			// Nothing reads ch any more, but a Broadcast may still be sending to it.
			go s.discard()
		}()

		s.pull(yield)
	}
}

// send hands m to the subscriber and reports whether it took it. It gives up once ctx is done or m's timeout runs out,
// or right away for a non-blocking message, then drops the reference the caller acquired.
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

// context returns the ctx m is handled and reported with: its own, or else the subscriber's.
func (s *Subscriber[T]) context(m message.Message[T]) context.Context {
	if m.Config.Context != nil {
		return m.Config.Context
	}

	return s.ctx
}

// report gives m to the subscriber's store, then passes err to the subscriber's error handler, then to m's, skipping
// whichever is not set, both with m's ctx. If the store fails, they are given a *StoreError wrapping err instead.
func (s *Subscriber[T]) report(m message.Message[T], err error) {
	ctx := s.context(m)
	if s.config.store != nil {
		// Never done, since ctx being done is often why m was lost.
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

// pull is Consume for Seq: it yields every message the subscriber takes, one at a time, until yield returns false, the
// subscriber's ctx is done, ch is closed or the subscriber is discarding. A message it takes once the subscriber is
// discarding is reported, and discard reports the rest.
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

// discard reads ch until it is closed, once a Seq loop has ended and unsubscribed, so that no Broadcast still sending
// to it waits for nothing. Every message it reads was taken but never yielded, and is reported as such.
func (s *Subscriber[T]) discard() {
	for m := range s.ch {
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value})
	}
}
