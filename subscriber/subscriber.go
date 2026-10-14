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

	// finished counts the subscriber out of its Broadcastor. Set by Attach.
	finished func()

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
	holder              Holder[T]

	// batchHandler is WithBatchHandler's handle, wrapped in its middlewares.
	batchHandler Handler[message.Batch[T]]

	errorHandler func(ctx context.Context, err error)
	filter       func(msg T) bool
	evict        func(err error) bool

	done []ctxchan
}

// ctxchan is a WithDone channel, and the ctx that bounds the send on it.
type ctxchan struct {
	ctx context.Context //nolint:containedctx // bounds a send at the end of the subscription
	ch  chan<- uuid.UUID
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

// Attach sets the subscriber's ctx, and its Broadcastor's remove, called once ctx is done, when a Seq loop ends and to
// evict, and finished, called once the subscriber is done, which must not block. It must be called once, first, and
// returns the subscriber's ctx: context.WithoutCancel(ctx) with WithDetachedContext.
func (s *Subscriber[T]) Attach(ctx context.Context, remove func(options ...UnsubscribeOption) bool, finished func()) context.Context {
	s.remove = remove
	s.finished = finished
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

	return s.deliver(ctx, message.New(value, s.config.defaults, config))
}

// DeliverBatch is Deliver for a batch, which must not be empty. Of a batch the filter keeps part of, it sends a batch
// from batch.New holding what it keeps.
func (s *Subscriber[T]) DeliverBatch(ctx context.Context, batch message.Batch[T], config message.Config) (bool, <-chan bool) {
	if s.config.filter != nil {
		if batch = s.keep(batch); batch == nil {
			return false, nil
		}
	}

	return s.deliver(ctx, message.NewBatch(batch, s.config.defaults, config))
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
// reports errors. It runs once, then sends its ID on each WithDone channel, then calls finished.
func (s *Subscriber[T]) Consume(handle Handler[T]) {
	defer s.finish()
	handle = chain(handle, s.config.middlewares...)
	if s.config.replay != nil {
		s.replay(handle)
	}
	if s.config.holder != nil {
		s.pace(handle)

		return
	}
	for m := range s.ch {
		s.took(&m)
		if s.discarding.Load() {
			s.report(m, s.closed(m))

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
		Queued:       max(len(s.ch)+int(s.counters.batched.Load()), 0),
		Buffer:       cap(s.ch),
		Sending:      int(s.counters.sending.Load()),
		Held:         int(s.counters.held.Load()),
		Delivered:    s.counters.delivered.Load(),
		Handled:      s.counters.handled.Load(),
		Failed:       s.counters.failed.Load(),
		TimedOut:     s.counters.timedOut.Load(),
		Dropped:      s.counters.dropped.Load(),
		Merged:       s.counters.merged.Load(),
		Batches:      s.counters.batches.Load(),
		HandleTime:   time.Duration(s.counters.handleTime.Load()),
		Handling:     s.counters.handling(s.created),
	}
}

// finish sends the ID on each WithDone channel in turn, each waiting for a receiver until its ctx is done, then calls
// finished, so that Shutdown returns after.
func (s *Subscriber[T]) finish() {
	for _, done := range s.config.done {
		// Tried first, since select picks at random: a ready receiver gets the ID even once ctx is done.
		select {
		case done.ch <- s.id:
			continue
		default:
		}
		select {
		case done.ch <- s.id:
		case <-done.ctx.Done():
		}
	}
	s.finished()
}

// pace is Consume's loop with a holder: it holds each message taken, and processes each one released once due, then
// everything still held once ch is closed. Only it calls the holder.
func (s *Subscriber[T]) pace(handle Handler[T]) {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		// A stale tick, from a timer that fired while a message came, only wakes the loop up once more.
		var due <-chan time.Time
		if wait := s.unhold(handle, time.Now(), false); wait > 0 {
			if timer == nil {
				timer = time.NewTimer(wait)
			} else {
				timer.Reset(wait)
			}
			due = timer.C
		}
		select {
		case m, ok := <-s.ch:
			if !ok {
				s.unhold(handle, time.Now(), true)

				return
			}
			s.took(&m)
			s.hold(handle, m, time.Now())
		case <-due:
		}
	}
}

// hold passes m to the holder at now, a batch one value at a time, or reports it once the subscriber discards. Before
// each value, it releases what is due at now, which may have come due while waiting or as the holder took the previous
// one, so that the holder never gets a message for a window that ended.
func (s *Subscriber[T]) hold(handle Handler[T], m message.Message[T], now time.Time) {
	if m.Batch == nil {
		s.holdValue(handle, m, now)

		return
	}
	s.unhold(handle, now, false)
	if s.discarding.Load() {
		s.report(m, s.closed(m))

		return
	}
	// Called rather than ranged over, which would allocate the loop's state too.
	m.Batch.All()(func(value T) bool {
		s.holdValue(handle, message.Message[T]{Value: value, Config: m.Config}, now)

		return true
	})
}

// holdValue releases what is due at now, then passes m, a single value, to the holder, or reports it once the
// subscriber discards.
func (s *Subscriber[T]) holdValue(handle Handler[T], m message.Message[T], now time.Time) {
	s.unhold(handle, now, false)
	if s.discarding.Load() {
		s.report(m, s.closed(m))

		return
	}
	merged, ok := s.config.holder.Hold(m, now)
	if !ok {
		s.counters.dropped.Add(1)
		s.lose(m, &DroppedError[T]{SubscriberID: s.id, Message: m.Value})

		return
	}
	merged = max(merged, 0)
	s.counters.merged.Add(uint64(merged))
	s.counters.held.Add(int64(1 - merged))
}

// unhold processes each message the holder releases, every one if all, else those due at now, or reports them once the
// subscriber discards. It returns how long until the next one is due, 0 if none will be until the next message.
func (s *Subscriber[T]) unhold(handle Handler[T], now time.Time, all bool) time.Duration {
	for {
		m, wait, ok := s.config.holder.Release(now, all)
		if !ok {
			return wait
		}
		s.counters.held.Add(-int64(m.Len()))
		if s.discarding.Load() {
			s.report(m, s.closed(m))

			continue
		}
		s.process(handle, m)
	}
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

// process passes m to handle, counts it, and reports its error. A batch goes to the batch handler, or else to handle
// one value at a time.
func (s *Subscriber[T]) process(handle Handler[T], m message.Message[T]) {
	if m.Batch != nil {
		s.unroll(handle, m)

		return
	}
	start := time.Now()
	s.counters.handleStart.Store(int64(start.Sub(s.created)) + 1)
	err := handle(s.context(m), s.id, m.Value)
	s.counters.handleStart.Store(0)
	s.counters.handle(time.Since(start), 1, err)
	// Reported outside the chain, so Recover never catches a panic in an error handler or evict.
	if err != nil {
		s.lose(m, err)
	}
}

// unroll passes the batch m to the batch handler, counting each value, or else each value to process, checking before
// each that the subscriber does not discard.
func (s *Subscriber[T]) unroll(handle Handler[T], m message.Message[T]) {
	if s.config.batchHandler == nil {
		// Called rather than ranged over, which would allocate the loop's state too.
		m.Batch.All()(func(value T) bool {
			v := message.Message[T]{Value: value, Config: m.Config}
			if s.discarding.Load() {
				s.report(v, s.closed(v))

				return true
			}
			s.process(handle, v)

			return true
		})

		return
	}
	start := time.Now()
	s.counters.handleStart.Store(int64(start.Sub(s.created)) + 1)
	err := s.config.batchHandler(s.context(m), s.id, m.Batch)
	s.counters.handleStart.Store(0)
	s.counters.batches.Add(1)
	s.counters.handle(time.Since(start), count(m), err)
	if err != nil {
		s.lose(m, err)
	}
}

// deliver sends m as its delivery says, for Deliver and DeliverBatch.
func (s *Subscriber[T]) deliver(ctx context.Context, m message.Message[T]) (bool, <-chan bool) {
	if !s.acquire() {
		s.report(m, s.closed(m)) //nolint:contextcheck // reported with the message's ctx

		return false, nil
	}

	if m.Config.Delivery == message.DeliveryAsync {
		if !s.reserve() {
			defer s.release()
			s.counters.dropped.Add(count(m))
			s.lose(m, &DroppedError[T]{SubscriberID: s.id, Message: m.Value, Batch: m.Batch}) //nolint:contextcheck // reported with the message's ctx

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

// send hands m over and releases the caller's reference. It gives up once ctx is done, m's timeout runs out or the
// subscriber is unsubscribed, or right away if m is non-blocking.
func (s *Subscriber[T]) send(ctx context.Context, m message.Message[T]) bool {
	defer s.release()

	// Checked first, since select picks at random: once unsubscribed, only a send racing Unsubscribe gets through.
	select {
	case <-s.done:
		s.report(m, s.closed(m)) //nolint:contextcheck // reported with the message's ctx

		return false
	default:
	}

	// Tried first, so that a ready subscriber costs neither a timer nor the select's locks, and takes m even if ctx is
	// done.
	select {
	case s.ch <- m:
		s.sent(&m)

		return true
	default:
	}

	if m.Config.Delivery == message.DeliveryNonBlocking {
		s.counters.dropped.Add(count(m))
		s.lose(m, &DroppedError[T]{SubscriberID: s.id, Message: m.Value, Batch: m.Batch}) //nolint:contextcheck // reported with the message's ctx

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
		s.sent(&m)

		return true
	case <-s.done:
		s.report(m, s.closed(m)) //nolint:contextcheck // reported with the message's ctx

		return false
	case <-sendCtx.Done():
		s.counters.timedOut.Add(count(m))
		s.lose(m, &TimeoutError[T]{SubscriberID: s.id, Message: m.Value, Batch: m.Batch, Err: sendCtx.Err()}) //nolint:contextcheck // reported with the message's ctx

		return false
	}
}

// sent counts m, just taken. A batch's values past its first are counted in Queued until Consume takes it.
func (s *Subscriber[T]) sent(m *message.Message[T]) {
	if m.Batch == nil {
		s.counters.delivered.Add(1)

		return
	}
	s.counters.batched.Add(int64(m.Batch.Len() - 1))
	s.counters.delivered.Add(count(*m))
}

// took uncounts from Queued the values of m, just read from ch, past its first.
func (s *Subscriber[T]) took(m *message.Message[T]) {
	if m.Batch != nil {
		s.counters.batched.Add(-int64(m.Batch.Len() - 1))
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
	s.report(m, &EvictedError[T]{SubscriberID: s.id, Message: m.Value, Batch: m.Batch, Err: err})
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

// report stores m, a batch one value at a time, then passes err to the subscriber's error handler, then to m's, once
// even for a batch. A failed Put turns err into a *StoreError.
func (s *Subscriber[T]) report(m message.Message[T], err error) {
	ctx := s.context(m)
	if s.config.store != nil {
		if putErr := s.put(ctx, m, err); putErr != nil {
			err = &StoreError[T]{SubscriberID: s.id, Message: m.Value, Batch: m.Batch, Err: putErr, Cause: err}
		}
	}
	if s.config.errorHandler != nil {
		s.config.errorHandler(ctx, err)
	}
	if m.Config.ErrorHandler != nil {
		m.Config.ErrorHandler(ctx, err)
	}
}

// put stores m, lost with err, or each value of its batch, and returns the errors of the Puts that failed.
func (s *Subscriber[T]) put(ctx context.Context, m message.Message[T], err error) error {
	// Never done: ctx being done is often why m was lost.
	ctx = context.WithoutCancel(ctx)
	if m.Batch == nil {
		return s.config.store.Put(ctx, Record[T]{SubscriberID: s.id, Message: m.Value, Err: err})
	}
	var errs []error
	for value := range m.Batch.All() {
		if putErr := s.config.store.Put(ctx, Record[T]{SubscriberID: s.id, Message: value, Err: err}); putErr != nil {
			errs = append(errs, putErr)
		}
	}

	return errors.Join(errs...)
}

// closed returns the error reporting m missed because the subscriber was unsubscribed.
func (s *Subscriber[T]) closed(m message.Message[T]) *ClosedError[T] {
	return &ClosedError[T]{SubscriberID: s.id, Message: m.Value, Batch: m.Batch}
}

// keep returns what of batch the filter keeps: batch itself if every value, nil if none, else a batch from batch.New.
// The filter sees each value once, since it may keep state.
func (s *Subscriber[T]) keep(batch message.Batch[T]) message.Batch[T] {
	var (
		kept message.Batch[T]
		// seen counts the values before the current one, all kept while kept is nil.
		seen int
	)
	for value := range batch.All() {
		switch {
		case s.config.filter(value):
			if kept != nil {
				kept.Add(value)
			}
		case kept == nil:
			kept = batch.New()
			for v := range batch.All() {
				if kept.Len() == seen {
					break
				}
				kept.Add(v)
			}
		}
		seen++
	}
	switch {
	case kept == nil:
		return batch
	case kept.Len() == 0:
		return nil
	default:
		return kept
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
