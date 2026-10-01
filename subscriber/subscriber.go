// Package subscriber holds the Subscriber a Broadcastor manages, the options passed to Subscribe, SubscribeSeq and
// Unsubscribe, Handler and Middleware, and the errors about a subscriber's messages.
package subscriber

import (
	"cmp"
	"context"
	"iter"
	"slices"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/message"
)

// Subscriber is one subscription of a Broadcastor. Its channel is read either by Consume or by a single Seq loop.
type Subscriber[T any] struct {
	id     uuid.UUID
	ch     chan message.Message[T]
	config config[T]

	// ctx handles and reports every message that has no ctx of its own. Set by Attach.
	ctx context.Context //nolint:containedctx // the subscription's, which outlives every call

	// remove removes the subscriber from its Broadcastor, as Broadcastor.Unsubscribe does. Set by Attach.
	remove func(options ...UnsubscribeOption) bool

	// done is closed by Unsubscribe, so that no send waits on a subscriber that is gone.
	done chan struct{}

	// discarding makes Consume and pull report messages instead of processing them.
	discarding atomic.Bool

	// stopContextLifetime cancels the AfterFunc set by Attach. nil with WithDetachedContext.
	stopContextLifetime func() bool

	// refs counts the subscription plus each Broadcast sending on ch. Whoever drops it to 0 closes ch, so ch is never
	// closed under a sender.
	refs atomic.Int64

	// misses counts the messages lost in a row, for WithEvictAfter.
	misses atomic.Int64

	// ordering holds messages until they are due. nil without WithOrder.
	ordering *ordering[T]

	// cutoff is the newest offset when the subscriber was stored, 0 for none: Deliver skips every message up to it,
	// which read returns once it has been appended. Set by Replay.
	cutoff uint64
	read   func(ctx context.Context) ([]HistoryEntry[T], error)

	// backlog holds what is left to replay. Set by ReadHistory, then only the reader uses it.
	backlog []T

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
	order               OrderPolicy[T]
	replay              bool
	keep                func(msg T) bool
}

// New returns a subscriber holding the subscription's reference, which Unsubscribe drops.
func New[T any](id uuid.UUID, options ...Option[T]) *Subscriber[T] {
	var config config[T]
	for _, option := range options {
		option(&config)
	}
	s := &Subscriber[T]{id: id, ch: make(chan message.Message[T], config.buffer), config: config, done: make(chan struct{})}
	s.refs.Store(1)
	if config.order.Compare != nil {
		s.ordering = newOrdering(config.order)
	}

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

// Replays reports whether the subscriber replays its Broadcastor's history (WithReplay).
func (s *Subscriber[T]) Replays() bool {
	return s.config.replay
}

// Replay makes the subscriber skip every message up to cutoff, the newest offset, and read them back with read in
// ReadHistory. It must be called before the subscriber is stored, so that every Broadcast that finds it skips them.
func (s *Subscriber[T]) Replay(cutoff uint64, read func(ctx context.Context) ([]HistoryEntry[T], error)) {
	s.cutoff, s.read = cutoff, read
}

// ReadHistory reads into the backlog, oldest first, the messages up to the cutoff that keep accepts: the later ones
// come live. A failure is reported as a *ReplayError, unless the subscription's ctx is done, which ends it anyway. It
// must be called once the subscriber is stored, before anything reads it, and does nothing without Replay.
func (s *Subscriber[T]) ReadHistory() {
	if s.read == nil {
		return
	}
	entries, err := s.read(s.ctx)
	if err != nil {
		// Not through report, since it is about no message to store.
		if s.ctx.Err() == nil && s.config.errorHandler != nil {
			s.config.errorHandler(s.ctx, &ReplayError{SubscriberID: s.id, Err: err})
		}

		return
	}
	entries = slices.DeleteFunc(entries, func(entry HistoryEntry[T]) bool {
		return entry.Offset > s.cutoff || (s.config.keep != nil && !s.config.keep(entry.Message))
	})
	slices.SortFunc(entries, func(a, b HistoryEntry[T]) int { return cmp.Compare(a.Offset, b.Offset) })
	s.backlog = make([]T, len(entries))
	for i, entry := range entries {
		s.backlog[i] = entry.Message
	}
}

// Deliver sends value, whose offset in the history is offset, 0 without one, to the subscriber and reports whether it
// took it, or, for an async message, whether a send was started. A parallel message returns false and a channel that
// yields the result instead. Failures are reported with the message's ctx, never ctx, which only bounds the wait.
func (s *Subscriber[T]) Deliver(ctx context.Context, offset uint64, value T, options ...message.Option[T]) (bool, <-chan bool) {
	// Read back from the history instead. Neither reported nor counted, since it is handled once, from there.
	if offset != 0 && offset <= s.cutoff {
		return false, nil
	}
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
	// Every send waiting on ch gives up. It still holds its reference, so ch is not closed under it.
	close(s.done)

	s.release()
}

// Consume calls handle, wrapped in the middlewares, for every message until ch is closed, and reports its errors.
func (s *Subscriber[T]) Consume(handle Handler[T]) {
	handle = chain(handle, s.config.middlewares...)
	if s.ordering == nil {
		for m, ok := s.replayed(); ok; m, ok = s.replayed() {
			s.process(handle, m)
		}
		for m := range s.ch {
			s.process(handle, m)
		}

		return
	}
	s.holdBacklog()
	for {
		m, ok := s.next(nil)
		if !ok {
			return
		}
		s.process(handle, m)
	}
}

// Seq returns SubscribeSeq's iterator, which can be ranged over once. When the loop ends, it removes the subscriber and
// reports what is left on ch.
func (s *Subscriber[T]) Seq() iter.Seq[T] {
	var ranged atomic.Bool

	return func(yield func(T) bool) {
		if ranged.Swap(true) {
			return
		}
		defer func() {
			s.remove()
			// In a goroutine: a Broadcast still sending here reports before it releases, and its error handler may be
			// waiting on this goroutine.
			go s.discard()
		}()

		s.pull(yield)
	}
}

// Stats returns a snapshot of the subscriber's counters.
func (s *Subscriber[T]) Stats() Stats {
	return Stats{
		SubscriberID: s.id,
		Queued:       len(s.ch),
		Buffer:       cap(s.ch),
		Delivered:    s.counters.delivered.Load(),
		Handled:      s.counters.handled.Load(),
		Failed:       s.counters.failed.Load(),
		TimedOut:     s.counters.timedOut.Load(),
		Dropped:      s.counters.dropped.Load(),
		Late:         s.counters.late.Load(),
		Held:         int(s.counters.held.Load()),
		HandleTime:   time.Duration(s.counters.handleTime.Load()),
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

// process calls handle for m and reports its error, or reports m once the subscriber is discarding.
func (s *Subscriber[T]) process(handle Handler[T], m message.Message[T]) {
	if s.discarding.Load() {
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value})

		return
	}
	start := time.Now()
	err := handle(s.context(m), s.id, m.Value)
	s.counters.handle(time.Since(start), err)
	// Reported outside the chain, so Recover never catches a panic in an error handler.
	if err != nil {
		s.report(m, err)
	}
}

// pull is Consume for Seq: it yields messages until yield returns false, ctx is done, ch is closed or the subscriber
// is discarding.
func (s *Subscriber[T]) pull(yield func(T) bool) {
	if s.ordering != nil {
		s.holdBacklog()
	}
	for s.ctx.Err() == nil && !s.discarding.Load() {
		m, ok := s.take()
		if !ok {
			return
		}
		if s.discarding.Load() {
			s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value})

			return
		}
		start := time.Now()
		more := yield(m.Value)
		s.counters.handle(time.Since(start), nil)
		if !more {
			return
		}
	}
}

// take returns the next message for pull, or false once ch is closed or ctx is done.
func (s *Subscriber[T]) take() (message.Message[T], bool) {
	if s.ordering != nil {
		return s.next(s.ctx.Done())
	}
	if m, ok := s.replayed(); ok {
		return m, true
	}
	select {
	case m, ok := <-s.ch:
		return m, ok
	case <-s.ctx.Done():
		return message.Message[T]{}, false
	}
}

// next returns the first message in order once it is due, or false once ch is closed and nothing is held, or once
// stop is closed. While nothing is due it takes messages, so that holding them does not hold Broadcast up. Once one is
// due, it takes none until none is, so that a slow handle holds Broadcast up as it would without WithOrder.
func (s *Subscriber[T]) next(stop <-chan struct{}) (message.Message[T], bool) {
	o := s.ordering
	for {
		var (
			m  message.Message[T]
			ok bool
		)
		if o.closed {
			m, ok = o.buffer.Release()
		} else {
			m, ok = o.buffer.Pop(time.Now())
		}
		if ok {
			s.counters.held.Add(-1)

			return m, true
		}
		if o.closed {
			o.stop()

			return m, false
		}

		// Before waiting, so that the messages queued together are ordered together.
		taken, open := s.takeQueued()
		if !open {
			o.closed = true

			continue
		}
		if taken {
			continue
		}

		select {
		case m, ok := <-s.ch:
			if !ok {
				o.closed = true

				continue
			}
			s.hold(m)
		case <-o.wait():
		case <-stop:
			o.stop()

			return message.Message[T]{}, false
		}
	}
}

// takeQueued holds the messages queued on ch, and reports whether it took any, and false once ch is closed. It takes as
// many as were queued when it started, plus one, so that it ends however fast messages come, and also takes one from a
// send waiting on an unbuffered ch.
func (s *Subscriber[T]) takeQueued() (bool, bool) {
	taken := false
	for range len(s.ch) + 1 {
		select {
		case m, ok := <-s.ch:
			if !ok {
				return taken, false
			}
			s.hold(m)
			taken = true
		default:
			return taken, true
		}
	}

	return taken, true
}

// hold holds m until it is due, or reports it as a *LateError, or as a *ClosedError once discarding.
func (s *Subscriber[T]) hold(m message.Message[T]) {
	if s.ordering.buffer.Push(m, time.Now()) {
		s.counters.held.Add(1)

		return
	}
	if s.discarding.Load() {
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value})

		return
	}
	last, _ := s.ordering.buffer.Last()
	s.counters.late.Add(1)
	s.report(m, &LateError[T]{SubscriberID: s.id, Message: m.Value, After: last.Value})
}

// replayed returns the next message of the backlog, counted as delivered, or false once there is none left.
func (s *Subscriber[T]) replayed() (message.Message[T], bool) {
	if len(s.backlog) == 0 {
		s.backlog = nil

		return message.Message[T]{}, false
	}
	v := s.backlog[0]
	var zero T
	s.backlog[0] = zero // so that it can be collected
	s.backlog = s.backlog[1:]
	s.counters.delivered.Add(1)

	return message.New(v, s.config.defaults), true
}


// holdBacklog holds the whole backlog, before any live message, so that they are merged in order.
func (s *Subscriber[T]) holdBacklog() {
	for m, ok := s.replayed(); ok; m, ok = s.replayed() {
		s.hold(m)
	}
}

// discard reports every message held, then what is left of the backlog, then every message left on ch until it is
// closed, so no Broadcast waits on a loop that has ended.
func (s *Subscriber[T]) discard() {
	if s.ordering != nil {
		for m, ok := s.ordering.buffer.Release(); ok; m, ok = s.ordering.buffer.Release() {
			s.counters.held.Add(-1)
			s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value})
		}
	}
	for m, ok := s.replayed(); ok; m, ok = s.replayed() {
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value})
	}
	for m := range s.ch {
		s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value})
	}
}
