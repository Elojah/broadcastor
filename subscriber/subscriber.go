// Package subscriber holds the Subscriber a Broadcastor manages, the options passed to Subscribe, SubscribeSeq and
// Unsubscribe, Handler and Middleware, and the errors about a subscriber's messages.
package subscriber

import (
	"context"
	"iter"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/message"
)

// Subscriber is one subscription of a Broadcastor. Consume reads its channel, for handle or for a single Seq loop.
type Subscriber[T any] struct {
	id     uuid.UUID
	ch     chan message.Message[T]
	config config[T]

	// created is what counters.handleStart counts from.
	created time.Time

	// ctx handles and reports every message that has no ctx of its own. Set by Attach.
	ctx context.Context //nolint:containedctx // the subscription's, which outlives every call

	// remove removes the subscriber from its Broadcastor, as Broadcastor.Unsubscribe does. Set by Attach.
	remove func(options ...UnsubscribeOption) bool

	// done is closed by Unsubscribe, so that no send waits on a subscriber that is gone.
	done chan struct{}

	// discarding makes Consume report messages instead of processing them, and ends a Seq loop.
	discarding atomic.Bool

	// stopContextLifetime cancels the AfterFunc set by Attach. nil with WithDetachedContext.
	stopContextLifetime func() bool

	// refs counts the subscription plus each Broadcast sending on ch. Whoever drops it to 0 closes ch, so ch is never
	// closed under a sender.
	refs atomic.Int64

	// misses counts the messages lost in a row, for WithEvictAfter.
	misses atomic.Int64

	counters counters
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
	evictAfter          int
	asyncLimit          int
	filter              func(msg T) bool
}

// New returns a subscriber holding the subscription's reference, which Unsubscribe drops.
func New[T any](id uuid.UUID, options ...Option[T]) *Subscriber[T] {
	var config config[T]
	for _, option := range options {
		option(&config)
	}
	s := &Subscriber[T]{
		id: id, ch: make(chan message.Message[T], config.buffer), config: config, created: time.Now(), done: make(chan struct{}),
	}
	s.refs.Store(1)

	return s
}

// ID returns the subscriber's ID.
func (s *Subscriber[T]) ID() uuid.UUID {
	return s.id
}

// Attach ties the subscriber to its Broadcastor, which remove removes it from. The subscriber runs with ctx, and calls
// remove once ctx is done, when a Seq loop ends, and to evict itself (WithEvictAfter). With WithDetachedContext, it
// runs with context.WithoutCancel(ctx) instead, and ctx never removes it. It returns the ctx the subscriber runs with,
// and must be called once, before anything else.
func (s *Subscriber[T]) Attach(ctx context.Context, remove func(options ...UnsubscribeOption) bool) context.Context {
	s.remove = remove
	if s.config.detached {
		s.ctx = context.WithoutCancel(ctx)

		return s.ctx
	}
	s.ctx = ctx
	s.stopContextLifetime = context.AfterFunc(ctx, func() { remove() })

	return ctx
}

// Deliver sends value to the subscriber, with config (message.NewConfig) laid over its defaults, and reports whether it
// took it, or, for an async message, whether a send was started. A parallel message returns false and a channel that
// yields the result instead. Failures are reported with the message's ctx, never ctx, which only bounds the wait.
// A message the filter (WithFilter) rejects returns false, and nothing else happens.
func (s *Subscriber[T]) Deliver(ctx context.Context, value T, config message.Config) (bool, <-chan bool) {
	if s.config.filter != nil && !s.config.filter(value) {
		return false, nil
	}
	m := message.New(value, s.config.defaults, config)

	if !s.acquire() {
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: value}) //nolint:contextcheck // reported with the message's ctx, not the one bounding the wait

		return false, nil
	}

	if m.Config.Delivery == message.DeliveryAsync {
		if !s.startSending() {
			defer s.release()
			s.counters.dropped.Add(1)
			s.lose(m, &DroppedError[T]{SubscriberID: s.id, Message: value})

			return false, nil
		}
		go func() {
			s.send(ctx, m)
			s.counters.sending.Add(-1)
		}()

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
	// Every send waiting on ch gives up. It still holds its reference, so ch is not closed under it.
	close(s.done)

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
		start := time.Now()
		s.counters.handleStart.Store(int64(start.Sub(s.created)) + 1)
		err := handle(s.context(m), s.id, m.Value)
		s.counters.handleStart.Store(0)
		s.counters.handle(time.Since(start), err)
		// Reported outside the chain, so Recover never catches a panic in an error handler.
		if err != nil {
			s.report(m, err)
		}
	}
}

// Seq returns SubscribeSeq's iterator, which can be ranged over once. Ranging starts Consume, whose handle passes each
// message to the loop, so the middlewares and error handlers apply as with Subscribe. When the loop ends, it removes
// the subscriber, and Consume reports what is left on ch.
func (s *Subscriber[T]) Seq() iter.Seq2[T, func(error)] {
	var ranged atomic.Bool

	return func(yield func(T, func(error)) bool) {
		if ranged.Swap(true) {
			return
		}
		r := newRelay[T]()
		go func() {
			s.Consume(r.handle)
			// Only handle sends on it, from Consume.
			close(r.messages)
		}()
		defer func() {
			// Before stopped, through which handle learns that the loop ended: Consume then reports every later message
			// itself, and only the one in handle goes through the middlewares.
			s.discarding.Store(true)
			close(r.stopped)
			s.remove()
		}()

		s.pull(r, yield)
	}
}

// Stats returns a snapshot of the subscriber's counters.
func (s *Subscriber[T]) Stats() Stats {
	return Stats{
		SubscriberID: s.id,
		Queued:       len(s.ch),
		Buffer:       cap(s.ch),
		Sending:      int(s.counters.sending.Load()),
		Delivered:    s.counters.delivered.Load(),
		Handled:      s.counters.handled.Load(),
		Failed:       s.counters.failed.Load(),
		TimedOut:     s.counters.timedOut.Load(),
		Dropped:      s.counters.dropped.Load(),
		HandleTime:   time.Duration(s.counters.handleTime.Load()),
		Handling:     s.counters.handling(s.created),
	}
}

// send hands m over and releases the caller's reference. It gives up once ctx is done, m's timeout runs out or the
// subscriber is unsubscribed, or right away if m is non-blocking.
func (s *Subscriber[T]) send(ctx context.Context, m message.Message[T]) bool {
	defer s.release()

	// Checked first, since select picks at random among ready cases: once unsubscribed, the subscriber takes no new
	// message, but for a send racing Unsubscribe.
	select {
	case <-s.done:
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value}) //nolint:contextcheck // reported with the message's ctx, not the one bounding the wait

		return false
	default:
	}

	// Before waiting, so that a subscriber ready for m costs neither a timer nor the locks of the select below, which
	// takes every channel's. Even once ctx is done, it takes m.
	select {
	case s.ch <- m:
		s.taken()

		return true
	default:
	}

	if m.Config.Delivery == message.DeliveryNonBlocking {
		s.counters.dropped.Add(1)
		s.lose(m, &DroppedError[T]{SubscriberID: s.id, Message: m.Value})

		return false
	}

	sendCtx := ctx
	if m.Config.Timeout > 0 {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(ctx, m.Config.Timeout)
		defer cancel()
	}

	select {
	case s.ch <- m:
		s.taken()

		return true
	case <-s.done:
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value}) //nolint:contextcheck // reported with the message's ctx, not the one bounding the wait

		return false
	case <-sendCtx.Done():
		s.counters.timedOut.Add(1)
		s.lose(m, &TimeoutError[T]{SubscriberID: s.id, Message: m.Value, Err: sendCtx.Err()})

		return false
	}
}

// taken counts a message the subscriber took, which ends a run of losses.
func (s *Subscriber[T]) taken() {
	s.counters.delivered.Add(1)
	if s.config.evictAfter > 0 {
		s.misses.Store(0)
	}
}

// lose reports m, lost to err, and evicts the subscriber once it has lost evictAfter messages in a row (WithEvictAfter).
// Only the loss whose remove deletes the subscriber is reported as an *EvictedError, once removed: so an eviction is
// reported once, and never for a subscriber something else removed first.
func (s *Subscriber[T]) lose(m message.Message[T], err error) {
	if s.config.evictAfter > 0 && s.misses.Add(1) >= int64(s.config.evictAfter) && s.remove(WithUnsubscribeDiscard()) {
		err = &EvictedError[T]{SubscriberID: s.id, Message: m.Value, Err: err}
	}
	s.report(m, err)
}

// startSending counts an async send under way, unless WithAsyncLimit's are already.
func (s *Subscriber[T]) startSending() bool {
	limit := int64(s.config.asyncLimit)
	if limit <= 0 {
		s.counters.sending.Add(1)

		return true
	}
	for {
		n := s.counters.sending.Load()
		if n >= limit {
			return false
		}
		if s.counters.sending.CompareAndSwap(n, n+1) {
			return true
		}
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

// pull yields each message r's handle passes on, and sends back the error the loop body passed to fail, until yield
// returns false, ctx is done, Consume has returned or the subscriber is discarding.
func (s *Subscriber[T]) pull(r *relay[T], yield func(T, func(error)) bool) {
	var err error
	fail := func(e error) { err = e }
	for s.ctx.Err() == nil && !s.discarding.Load() {
		select {
		case msg, ok := <-r.messages:
			// If discarding, handle gets a *ClosedError once the loop ends.
			if !ok || s.discarding.Load() {
				return
			}
			err = nil
			more := yield(msg, fail)
			if !more {
				// Before handle returns, so that Consume reports what is left instead of passing it on.
				s.discarding.Store(true)
			}
			r.errs <- err
			if !more {
				return
			}
		case <-s.ctx.Done():
			return
		}
	}
}
