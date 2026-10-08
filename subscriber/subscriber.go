// Package subscriber holds the subscriber options, Handler and Middleware, Store, Stats, the errors about messages, and
// the Subscriber a Broadcastor manages.
package subscriber

import (
	"context"
	"errors"
	"iter"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/message"
)

// Subscriber is one subscription of a Broadcastor. Only Consume reads its channel.
type Subscriber[T any] struct {
	id     uuid.UUID
	ch     chan message.Message[T]
	config config[T]

	// created is what counters.handleStart counts from.
	created time.Time

	// ctx is for every message without one of its own. Set by Attach.
	ctx context.Context //nolint:containedctx // the subscription's, which outlives every call

	// remove removes the subscriber from its Broadcastor, and reports whether this call did. Set by Attach.
	remove func(options ...UnsubscribeOption) bool

	// stop tells the Broadcastor the subscriber is done. Set by Attach, called last by Consume.
	stop func()

	// done is closed by Unsubscribe, so that no send waits on a subscriber that is gone.
	done chan struct{}

	// discarding makes Consume report messages instead of handling them, and ends a Seq loop.
	discarding atomic.Bool

	// stopContextLifetime cancels the AfterFunc set by Attach. nil with WithDetachedContext.
	stopContextLifetime func() bool

	// refs counts the subscription plus each send on ch. Whoever drops it to 0 closes ch, so never under a sender.
	refs atomic.Int64

	counters counters
}

// config is what Options set.
type config[T any] struct {
	buffer              int
	defaults            message.Config
	unsubscribeDefaults unsubscription
	detached            bool
	store               Store[T]
	middlewares         []Middleware[T]
	asyncLimit          int
	replay              iter.Seq[T]

	errorHandler func(ctx context.Context, err error)
	filter       func(msg T) bool
	evict        func(err error) bool

	done []chan<- uuid.UUID
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

// Attach sets the subscriber's ctx, and its Broadcastor's callbacks: remove, called once ctx is done, when a Seq loop
// ends and to evict, and stop, called last by Consume. It must be called once, first, and returns the subscriber's
// ctx: context.WithoutCancel(ctx) with WithDetachedContext.
func (s *Subscriber[T]) Attach(ctx context.Context, remove func(options ...UnsubscribeOption) bool, stop func()) context.Context {
	s.remove = remove
	s.stop = stop
	if s.config.detached {
		s.ctx = context.WithoutCancel(ctx)

		return s.ctx
	}
	s.ctx = ctx
	s.stopContextLifetime = context.AfterFunc(ctx, func() { remove() })

	return ctx
}

// Deliver sends value, with config laid over the subscriber's defaults, and reports whether it was taken, or for async,
// whether a send started. A parallel send returns false and a channel yielding that instead. A value the filter
// rejects returns false and does nothing else. ctx only bounds the wait.
func (s *Subscriber[T]) Deliver(ctx context.Context, value T, config message.Config) (bool, <-chan bool) {
	if s.config.filter != nil && !s.config.filter(value) {
		return false, nil
	}
	m := message.New(value, s.config.defaults, config)

	if !s.acquire() {
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: value}) //nolint:contextcheck // reported with the message's ctx

		return false, nil
	}

	if m.Config.Delivery == message.DeliveryAsync {
		if !s.reserve() {
			defer s.release()
			s.counters.dropped.Add(1)
			s.lose(m, &DroppedError[T]{SubscriberID: s.id, Message: value}) //nolint:contextcheck // reported with the message's ctx

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
// subscriber calls it, once.
func (s *Subscriber[T]) Unsubscribe(options ...UnsubscribeOption) {
	u := s.config.unsubscribeDefaults
	for _, option := range options {
		option(&u)
	}
	// Before release, which may close ch right away: the reader must see it for every buffered message.
	if u.discard {
		s.discarding.Store(true)
	}
	// However it was removed, so the AfterFunc does not outlive it.
	if s.stopContextLifetime != nil {
		s.stopContextLifetime()
	}
	// Every send waiting on ch gives up. It still holds its reference, so ch is not closed under it.
	close(s.done)

	s.release()
}

// Consume calls handle, wrapped in the middlewares, for each replayed value then each message until ch closes, and
// reports errors. It runs once, then sends its ID on each WithDone channel and calls stop.
func (s *Subscriber[T]) Consume(handle Handler[T]) {
	defer s.finish()
	handle = chain(handle, s.config.middlewares...)
	if s.config.replay != nil {
		s.replay(handle)
	}
	for m := range s.ch {
		if s.discarding.Load() {
			s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value})

			continue
		}
		s.process(handle, m)
	}
}

// Seq returns SubscribeSeq's iterator, which ranges once. Ranging starts Consume, whose handle relays each message to
// the loop, so the middlewares and error handlers apply as with Subscribe. Ending the loop removes the subscriber.
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
			// Before stopped, so that only the message in handle goes through the middlewares: Consume reports the rest.
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

// finish sends the ID on each WithDone channel, without blocking, then calls stop, so that Shutdown returns after.
func (s *Subscriber[T]) finish() {
	for _, done := range s.config.done {
		select {
		case done <- s.id:
		default:
		}
	}
	s.stop()
}

// replay processes each replayed value like a message, until the subscriber discards.
func (s *Subscriber[T]) replay(handle Handler[T]) {
	for value := range s.config.replay {
		if s.discarding.Load() {
			return
		}
		s.counters.delivered.Add(1)
		s.process(handle, message.New(value, s.config.defaults, message.Config{}))
	}
}

// process passes m to handle, counts it, and reports its error.
func (s *Subscriber[T]) process(handle Handler[T], m message.Message[T]) {
	start := time.Now()
	s.counters.handleStart.Store(int64(start.Sub(s.created)) + 1)
	err := handle(s.context(m), s.id, m.Value)
	s.counters.handleStart.Store(0)
	s.counters.handle(time.Since(start), err)
	// Reported outside the chain, so Recover never catches a panic in an error handler or evict.
	if err != nil {
		s.lose(m, err)
	}
}

// send hands m over and releases the caller's reference. It gives up once ctx is done, m's timeout runs out or the
// subscriber is unsubscribed, or right away if m is non-blocking.
func (s *Subscriber[T]) send(ctx context.Context, m message.Message[T]) bool {
	defer s.release()

	// Checked first, since select picks at random: once unsubscribed, only a send racing Unsubscribe gets through.
	select {
	case <-s.done:
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value}) //nolint:contextcheck // reported with the message's ctx

		return false
	default:
	}

	// Tried first, so that a ready subscriber costs neither a timer nor the select's locks, and takes m even if ctx is
	// done.
	select {
	case s.ch <- m:
		s.counters.delivered.Add(1)

		return true
	default:
	}

	if m.Config.Delivery == message.DeliveryNonBlocking {
		s.counters.dropped.Add(1)
		s.lose(m, &DroppedError[T]{SubscriberID: s.id, Message: m.Value}) //nolint:contextcheck // reported with the message's ctx

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
		s.counters.delivered.Add(1)

		return true
	case <-s.done:
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value}) //nolint:contextcheck // reported with the message's ctx

		return false
	case <-sendCtx.Done():
		s.counters.timedOut.Add(1)
		s.lose(m, &TimeoutError[T]{SubscriberID: s.id, Message: m.Value, Err: sendCtx.Err()}) //nolint:contextcheck // reported with the message's ctx

		return false
	}
}

// lose reports m, lost or failed with err, and evicts the subscriber if evict picks err. Only the call whose remove
// succeeds reports an *EvictedError, so a subscriber is evicted once, and never once removed. A *ClosedError never
// reaches evict: an ended Seq loop's would race its own removal.
func (s *Subscriber[T]) lose(m message.Message[T], err error) {
	if s.config.evict == nil || errors.Is(err, ErrClosed) || !s.config.evict(err) || !s.remove(WithUnsubscribeDiscard()) {
		s.report(m, err)

		return
	}
	s.report(m, &EvictedError[T]{SubscriberID: s.id, Message: m.Value, Err: err})
}

// reserve counts an async send under way, unless WithAsyncLimit's are already.
func (s *Subscriber[T]) reserve() bool {
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

// pull yields each relayed message and sends back the body's error, until the loop breaks, ctx is done, Consume
// returns or the subscriber discards.
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
				// Before handle returns, so that Consume reports what is left.
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
