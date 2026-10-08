package broadcastor

import (
	"bytes"
	"context"
	"iter"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/pkg/gate"
	"github.com/elojah/broadcastor/subscriber"
)

// Broadcastor hands each message to every subscriber. Create one with NewBroadcastor. It is safe for concurrent use,
// from handle included.
type Broadcastor[T any] struct {
	subscribers sync.Map // [uuid.UUID]*subscriber.Subscriber[T]

	// gate makes Close wait for any add under way, so Close refuses or sees each subscriber.
	gate gate.Gate

	// running counts the subscribers not done yet, plus one until the first Close. Only add raises it, under gate, so
	// it reaches 0 once, after Close, and whoever drops it there closes stopped.
	running atomic.Int64
	stopped chan struct{}
}

// NewBroadcastor returns an empty Broadcastor.
func NewBroadcastor[T any]() *Broadcastor[T] {
	b := &Broadcastor[T]{stopped: make(chan struct{})}
	b.running.Store(1)

	return b
}

// Subscribe adds a subscriber and returns its ID. Its goroutine calls handle for each message, one at a time, with the
// ID so that handle can unsubscribe itself. A Broadcast under way may or may not reach it.
//
// ctx is the subscription's lifetime, unless subscriber.WithDetachedContext: once it is done, the subscriber is
// unsubscribed. handle gets ctx, unless the message has its own (message.WithContext).
//
// It returns ErrClosed after Close.
func (b *Broadcastor[T]) Subscribe(ctx context.Context, handle func(ctx context.Context, id uuid.UUID, msg T) error, options ...subscriber.Option[T]) (uuid.UUID, error) {
	s, err := b.add(ctx, options)
	if err != nil {
		return uuid.Nil, err
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
func (b *Broadcastor[T]) SubscribeSeq(ctx context.Context, options ...subscriber.Option[T]) (uuid.UUID, iter.Seq2[T, func(error)], error) {
	s, err := b.add(ctx, options)
	if err != nil {
		return uuid.Nil, nil, err
	}
	id := s.ID()

	return id, s.Seq(), nil
}

// Unsubscribe removes the subscriber, or returns a *SubscriberNotFoundError. It never waits, so handle can call it:
// the subscriber may still handle what it took, and a Broadcast waiting on it gives up with a
// *subscriber.ClosedError. options override subscriber.WithUnsubscribeOptions. ctx is unused.
func (b *Broadcastor[T]) Unsubscribe(ctx context.Context, id uuid.UUID, options ...subscriber.UnsubscribeOption) error {
	if !b.remove(id, options...) {
		return &SubscriberNotFoundError{SubscriberID: id}
	}

	return nil
}

// Close unsubscribes every subscriber, after which Subscribe and SubscribeSeq return ErrClosed. It never waits, so
// handle can call it. Every call after the first returns ErrClosed.
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
	b.stop()

	return nil
}

// Shutdown is Close, then waits until every subscriber is done: it has handled or discarded what it took, and sent its
// ID on its subscriber.WithDone channels. It returns ctx.Err() if ctx is done first, else Close's error.
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
	var (
		config = message.NewConfig(options...)
		n      int
		// Whether each parallel send was taken.
		pending []<-chan bool
	)
	b.subscribers.Range(func(_, value any) bool {
		s, ok := value.(*subscriber.Subscriber[T])
		if !ok || (config.SubscriberFilter != nil && !config.SubscriberFilter(s.ID())) {
			return true
		}
		taken, parallel := s.Deliver(ctx, msg, config)
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
	// UUIDv7s sort in creation order.
	slices.SortFunc(stats, func(x, y subscriber.Stats) int {
		return bytes.Compare(x.SubscriberID[:], y.SubscriberID[:])
	})

	return stats
}

// add creates and stores a subscriber, or returns ErrClosed. Nothing reads its channel yet.
func (b *Broadcastor[T]) add(ctx context.Context, options []subscriber.Option[T]) (*subscriber.Subscriber[T], error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	s := subscriber.New(id, options...)

	if !b.gate.Enter() {
		return nil, ErrClosed
	}
	defer b.gate.Leave()
	b.running.Add(1)

	// Before Store, so that whoever removes the subscriber stops its ctx watch.
	ctx = s.Attach(ctx, func(options ...subscriber.UnsubscribeOption) bool { return b.remove(id, options...) }, b.stop)
	b.subscribers.Store(id, s)
	// If ctx was already done, the watch may have run before Store and found nothing.
	if ctx.Err() != nil {
		b.remove(id)
	}

	return s, nil
}

// remove deletes the subscriber and unsubscribes it, or returns false if there is none. Only the deleter unsubscribes,
// so racing removals do it once.
func (b *Broadcastor[T]) remove(id uuid.UUID, options ...subscriber.UnsubscribeOption) bool {
	v, _ := b.subscribers.LoadAndDelete(id)
	s, ok := v.(*subscriber.Subscriber[T])
	if !ok {
		return false
	}
	s.Unsubscribe(options...)

	return true
}

// stop drops running by one, for a subscriber done or the first Close, and closes stopped at 0.
func (b *Broadcastor[T]) stop() {
	if b.running.Add(-1) == 0 {
		close(b.stopped)
	}
}
