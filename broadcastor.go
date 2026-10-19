package broadcastor

import (
	"cmp"
	"context"
	"iter"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/elojah/broadcastor/v2/message"
	"github.com/elojah/broadcastor/v2/pkg/gate"
	"github.com/elojah/broadcastor/v2/subscriber"
)

// Broadcastor hands each message to every subscriber. Create one with NewBroadcastor. It is safe for concurrent use,
// from handle included.
type Broadcastor[T any] struct {
	subscribers sync.Map // [uint64]*subscriber.Subscriber[T]

	// gate makes Close wait for any add under way, so Close refuses or sees each subscriber.
	gate gate.Gate

	// running counts the subscribers not done yet, plus one until the first Close. Only add raises it, under gate, so
	// it reaches 0 once, after Close, and countOut closes stopped then.
	running atomic.Int64
	stopped chan struct{}

	// ids is the last ID given out.
	ids atomic.Uint64

	// turn, set by WithSerial, holds a token while a Broadcast runs. A channel rather than a mutex, so that Broadcasts
	// take turns in the order they come, and one waiting blocks durably in a synctest bubble.
	turn chan struct{}

	// defaults, set by WithDefaultSubscriberOptions, build each subscriber's options, which come before its own.
	defaults []func() []subscriber.Option[T]
}

// Option configures a Broadcastor, when passed to NewBroadcastor.
type Option[T any] func(b *Broadcastor[T])

// NewBroadcastor returns an empty Broadcastor.
func NewBroadcastor[T any](options ...Option[T]) *Broadcastor[T] {
	b := &Broadcastor[T]{stopped: make(chan struct{})}
	b.running.Store(1)
	for _, option := range options {
		option(b)
	}

	return b
}

// WithSerial makes each Broadcast and BroadcastBatch wait, whatever its ctx, until the ones under way have returned,
// so that every subscriber takes messages in the same order, async sends aside. A Broadcast from a filter, or from an
// error handler a Broadcast runs, waits for good, and one from handle until the Broadcast waiting on that subscriber
// gives up.
func WithSerial[T any]() Option[T] {
	return func(b *Broadcastor[T]) {
		b.turn = make(chan struct{}, 1)
	}
}

// WithDefaultSubscriberOptions gives each subscriber the options build returns, before its Subscribe's own: those
// override a value, such as subscriber.WithBuffer's, and come after in a list, such as subscriber.WithMiddleware's.
// build runs on each Subscribe and SubscribeSeq, so that each subscriber gets its own filter or holder. Several run in
// order, and a nil build is ignored.
func WithDefaultSubscriberOptions[T any](build func() []subscriber.Option[T]) Option[T] {
	return func(b *Broadcastor[T]) {
		if build != nil {
			b.defaults = append(b.defaults, build)
		}
	}
}

// Subscribe adds a subscriber and returns its ID. Its goroutine calls handle for each message, one at a time, with the
// ID so that handle can unsubscribe itself. A Broadcast under way may or may not reach it. IDs count up from 1 in each
// Broadcastor, so 0 is never one.
//
// ctx is the subscription's lifetime, unless subscriber.WithDetachedContext: once it is done, the subscriber is
// unsubscribed. handle gets ctx, unless the message has its own (message.WithContext).
//
// It returns ErrClosed after Close.
func (b *Broadcastor[T]) Subscribe(ctx context.Context, handle func(ctx context.Context, id uint64, msg T) error, options ...subscriber.Option[T]) (uint64, error) {
	s, err := b.add(ctx, options)
	if err != nil {
		return 0, err
	}
	go s.Consume(handle)

	return s.ID(), nil
}

// SubscribeSeq is Subscribe with an iterator, whose loop body replaces handle, in the caller's goroutine. fail takes
// the error handle would return, and must be called before the body returns: not calling it means nil. The
// middlewares wrap the body, but a panic in it reaches the caller. Until the loop starts, Broadcast waits for it as
// for a busy handle.
//
// The loop ends on break, once ctx is done, or once unsubscribed and done with what it took. Ending unsubscribes, and
// reports what was taken but not yielded as a *subscriber.ClosedError. seq can be ranged over once.
//
// It returns ErrClosed after Close.
func (b *Broadcastor[T]) SubscribeSeq(ctx context.Context, options ...subscriber.Option[T]) (uint64, iter.Seq2[T, func(error)], error) {
	s, err := b.add(ctx, options)
	if err != nil {
		return 0, nil, err
	}
	id := s.ID()

	return id, s.Seq(), nil
}

// Unsubscribe removes the subscriber, or returns a *SubscriberNotFoundError. It never waits, so handle can call it:
// the subscriber may still handle what it took, and a Broadcast waiting on it gives up with a
// *subscriber.ClosedError. options override subscriber.WithUnsubscribeOptions. ctx is unused.
func (b *Broadcastor[T]) Unsubscribe(ctx context.Context, id uint64, options ...subscriber.UnsubscribeOption) error {
	if !b.remove(id, options...) {
		return &SubscriberNotFoundError{SubscriberID: id}
	}

	return nil
}

// Close unsubscribes every subscriber, after which Subscribe and SubscribeSeq return ErrClosed. It never waits for a
// subscriber, so handle can call it. Every call after the first returns ErrClosed.
func (b *Broadcastor[T]) Close() error {
	closed := b.gate.Close()

	// On every call, so no subscriber is left once any Close returns.
	b.subscribers.Range(func(_, value any) bool {
		if s, ok := value.(*subscriber.Subscriber[T]); ok {
			b.remove(s.ID())
		}

		return true
	})

	if closed {
		return ErrClosed
	}
	// Drops Close's one from running.
	b.countOut()

	return nil
}

// Shutdown is Close, then waits until every subscriber is done: it has handled or discarded what it took, and its
// subscriber.WithDone channels have received its ID or their ctx is done. It returns ctx.Err() if ctx is done first,
// else Close's error.
//
// Called from handle or an error handler, or with a SubscribeSeq loop never ranged, it waits until ctx is done. A
// Broadcast racing it may still report a *subscriber.ClosedError after it returns.
func (b *Broadcastor[T]) Shutdown(ctx context.Context) error {
	err := b.Close()

	// Checked first, since select picks at random among ready cases.
	select {
	case <-b.stopped:
		return err
	default:
	}

	select {
	case <-b.stopped:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Broadcast hands msg to every subscriber the message picks (message.WithSubscriberFilter) and whose own filter keeps
// it (subscriber.WithFilter), and returns how many took it, counting an async send once started.
//
// By default it waits for each subscriber in turn, and gives up on one once ctx is done or the message's timeout runs
// out, reporting why. A ready subscriber takes msg even then. ctx never reaches handle or the error handlers, but
// async sends keep using it after Broadcast returns.
func (b *Broadcastor[T]) Broadcast(ctx context.Context, msg T, options ...message.Option[T]) int {
	config := message.NewConfig(options...)

	return b.each(config, func(s *subscriber.Subscriber[T]) (bool, <-chan bool) {
		return s.Deliver(ctx, msg, config)
	})
}

// BroadcastBatch is Broadcast for every value of batch at once, with one Config: a subscriber takes the whole batch in
// one send, or misses it, and hands it to its batch handler (subscriber.WithBatchHandler), else to handle one value at
// a time. Each value counts in Stats as a message, but an error about the whole batch is reported once, with the batch
// in it.
//
// Every subscriber reads batch, at once and after an async BroadcastBatch returns, so batch must not change once
// passed. An empty or nil batch is not sent.
func (b *Broadcastor[T]) BroadcastBatch(ctx context.Context, batch message.Batch[T], options ...message.Option[T]) int {
	if batch == nil || batch.Len() == 0 {
		return 0
	}
	config := message.NewConfig(options...)

	return b.each(config, func(s *subscriber.Subscriber[T]) (bool, <-chan bool) {
		return s.DeliverBatch(ctx, batch, config)
	})
}

// Stats returns a snapshot of every subscriber's counters, in subscription order. An unsubscribed subscriber is left
// out, even while it still handles what it took. It never waits, so handle can call it.
func (b *Broadcastor[T]) Stats() []subscriber.Stats {
	var stats []subscriber.Stats
	b.subscribers.Range(func(_, value any) bool {
		if s, ok := value.(*subscriber.Subscriber[T]); ok {
			stats = append(stats, s.Stats())
		}

		return true
	})
	// IDs go up in subscription order.
	slices.SortFunc(stats, func(x, y subscriber.Stats) int {
		return cmp.Compare(x.SubscriberID, y.SubscriberID)
	})

	return stats
}

// add creates and stores a subscriber, or returns ErrClosed. Nothing reads its channel yet.
func (b *Broadcastor[T]) add(ctx context.Context, options []subscriber.Option[T]) (*subscriber.Subscriber[T], error) {
	// Before Enter, since build may call Subscribe or Close.
	if len(b.defaults) > 0 {
		all := make([][]subscriber.Option[T], 0, len(b.defaults)+1)
		for _, build := range b.defaults {
			all = append(all, build())
		}
		options = slices.Concat(append(all, options)...)
	}
	id := b.ids.Add(1)
	s := subscriber.New(id, options...)

	if !b.gate.Enter() {
		return nil, ErrClosed
	}
	defer b.gate.Leave()
	b.running.Add(1)

	// Before Store, so that whoever removes the subscriber stops its ctx watch.
	ctx = s.Attach(ctx, func(options ...subscriber.UnsubscribeOption) bool { return b.remove(id, options...) }, b.countOut)
	b.subscribers.Store(id, s)
	// If ctx was already done, the watch may have run before Store and found nothing.
	if ctx.Err() != nil {
		b.remove(id)
	}

	return s, nil
}

// each delivers to every subscriber config picks, and returns how many took it, waiting for each parallel send. With
// WithSerial, it first waits for its turn.
func (b *Broadcastor[T]) each(config message.Config, deliver func(s *subscriber.Subscriber[T]) (bool, <-chan bool)) int {
	if b.turn != nil {
		b.turn <- struct{}{}
		// Deferred, so that a filter's panic gives the turn back.
		defer func() { <-b.turn }()
	}

	var (
		n int
		// Whether each parallel send was taken.
		pending []<-chan bool
	)
	b.subscribers.Range(func(_, value any) bool {
		s, ok := value.(*subscriber.Subscriber[T])
		if !ok || (config.SubscriberFilter != nil && !config.SubscriberFilter(s.ID())) {
			return true
		}
		taken, parallel := deliver(s)
		if taken {
			n++
		}
		if parallel != nil {
			pending = append(pending, parallel)
		}

		return true
	})

	// Each send gives up on its own, so this never waits for another Broadcast.
	for _, taken := range pending {
		if <-taken {
			n++
		}
	}

	return n
}

// remove deletes the subscriber and unsubscribes it, or returns false if there is none. Only the deleter unsubscribes,
// so racing removals do it once.
func (b *Broadcastor[T]) remove(id uint64, options ...subscriber.UnsubscribeOption) bool {
	v, _ := b.subscribers.LoadAndDelete(id)
	s, ok := v.(*subscriber.Subscriber[T])
	if !ok {
		return false
	}
	s.Unsubscribe(options...)

	return true
}

// countOut drops running by one, and closes stopped at 0. Subscribers call it once done (subscriber.Subscriber.Attach).
func (b *Broadcastor[T]) countOut() {
	if b.running.Add(-1) == 0 {
		close(b.stopped)
	}
}
