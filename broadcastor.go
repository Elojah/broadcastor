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

// Broadcastor hands every message to each of its subscribers. Create one with NewBroadcastor. It is safe for concurrent
// use, including from handle.
type Broadcastor[T any] struct {
	subscribers sync.Map // [uuid.UUID]*subscriber.Subscriber[T]

	// gate makes Close wait for every add under way, so each subscriber is either refused or seen by Close.
	gate gate.Gate

	// running counts each subscriber whose Consume has not returned, plus one until the first Close. Only add counts one
	// in, under gate, so it reaches 0 once, after Close: whoever drops it to 0 closes stopped.
	running atomic.Int64
	stopped chan struct{}
	// exit is stop, which add passes to each subscriber, made once: a method value made in add would allocate.
	exit func()
}

// NewBroadcastor returns an empty Broadcastor.
func NewBroadcastor[T any]() *Broadcastor[T] {
	b := &Broadcastor[T]{stopped: make(chan struct{})}
	b.running.Store(1)
	b.exit = b.stop

	return b
}

// Subscribe adds a subscriber and returns its ID. The subscriber's own goroutine calls handle for each message, one at
// a time, with that ID so that handle can unsubscribe itself.
//
// ctx is the subscription's lifetime: once it is done, the subscriber is unsubscribed, even while handle runs, unless
// it has subscriber.WithDetachedContext. handle gets ctx, unless the message has its own (message.WithContext).
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

// SubscribeSeq is Subscribe with an iterator instead of handle: the loop body takes handle's place, in the caller's
// goroutine. It gets each message with fail, which takes the error handle would return, nil until called. fail must be
// called before the iteration ends. The middlewares wrap the loop body, so middleware.Retry yields a message again,
// but a panic in the body reaches the loop's caller, never middleware.Recover.
//
// Ranging starts the subscriber's goroutine, which hands each message to the loop. Until then, Broadcast waits for it
// as for a busy handle.
//
// The loop ends when it breaks, when ctx is done, or once the subscriber is unsubscribed and has yielded what it took.
// Ending unsubscribes it, and reports what it took but did not yield as *subscriber.ClosedError. The message being
// handed to the loop then, or whose body panicked, is that ClosedError's handle error, through the middlewares. seq can
// be ranged over once.
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

// Unsubscribe removes the subscriber, or returns a *SubscriberNotFoundError. It never waits, so handle can call it, and
// the subscriber may still process messages it already took. A Broadcast waiting on the subscriber gives up, and
// reports its message as a *subscriber.ClosedError. options override its subscriber.WithUnsubscribeOptions. ctx is
// unused.
func (b *Broadcastor[T]) Unsubscribe(ctx context.Context, id uuid.UUID, options ...subscriber.UnsubscribeOption) error {
	if !b.remove(id, options...) {
		return &SubscriberNotFoundError{SubscriberID: id}
	}

	return nil
}

// Close unsubscribes every subscriber, and makes later Subscribe and SubscribeSeq calls return ErrClosed. Like
// Unsubscribe it never waits, so handle can call it, whereas Shutdown waits for the subscribers. Every call after the
// first, Shutdown's included, returns ErrClosed.
func (b *Broadcastor[T]) Close() error {
	closed := b.gate.Close()

	// On every call, not just the first, so no subscriber is left once any Close returns.
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

// Shutdown is Close, then waits until every subscriber has handled what it took, or reported it as
// subscriber.WithUnsubscribeDiscard makes it, so that a program can exit right after, such as on SIGTERM. It returns
// ctx's error if ctx is done first, else what Close returned.
//
// It waits for a Broadcast only through a subscriber, which Close frees right away, unless an error handler or store
// blocks. A Broadcast racing Shutdown may still report its message as a *subscriber.ClosedError once it has returned.
// Called from handle or an error handler, it waits on itself until ctx is done, and so it does for a SubscribeSeq loop
// that was never started.
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

// Broadcast hands msg to every subscriber and returns how many took it, counting every async send started as taken, and
// none that skipped it (subscriber.WithFilter).
//
// By default it waits for each subscriber in turn, so a slow one holds up those after it. It gives up on a subscriber
// once ctx is done or the message's timeout runs out, and tells its error handlers why, but a subscriber ready for the
// message takes it even then. ctx only bounds the wait: it reaches neither handle nor the error handlers, although
// async sends keep using it (see message.WithAsync).
func (b *Broadcastor[T]) Broadcast(ctx context.Context, msg T, options ...message.Option[T]) int {
	var (
		// Once, rather than per subscriber, and none without options.
		config = message.NewConfig(options...)
		n      int
		// One per parallel send, yielding whether the subscriber took the message.
		pending []<-chan bool
	)
	b.subscribers.Range(func(_, value any) bool {
		s, ok := value.(*subscriber.Subscriber[T])
		if !ok {
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

// Stats returns a snapshot of every subscriber's counters, in the order they subscribed. A subscriber leaves it once
// unsubscribed, even while it still processes the messages it took. It never waits, so handle can call it.
func (b *Broadcastor[T]) Stats() []subscriber.Stats {
	var stats []subscriber.Stats
	b.subscribers.Range(func(_, value any) bool {
		if s, ok := value.(*subscriber.Subscriber[T]); ok {
			stats = append(stats, s.Stats())
		}

		return true
	})
	// IDs are UUIDv7, which sort in the order they were made.
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

	// Before Store, so whoever removes the subscriber stops the watch.
	ctx = s.Attach(ctx, func(options ...subscriber.UnsubscribeOption) bool { return b.remove(id, options...) }, b.exit)
	b.subscribers.Store(id, s)
	// If ctx was already done, the watch may have run before Store and found nothing.
	if ctx.Err() != nil {
		b.remove(id)
	}

	return s, nil
}

// remove deletes the subscriber and drops the subscription's reference, or reports false if there is none. Only
// whoever deletes it drops the reference, so racing removals drop it once.
func (b *Broadcastor[T]) remove(id uuid.UUID, options ...subscriber.UnsubscribeOption) bool {
	v, _ := b.subscribers.LoadAndDelete(id)
	s, ok := v.(*subscriber.Subscriber[T])
	if !ok {
		return false
	}
	s.Unsubscribe(options...)

	return true
}

// stop drops a count of running, for a Consume that returned or for the first Close, and closes stopped if it was the
// last.
func (b *Broadcastor[T]) stop() {
	if b.running.Add(-1) == 0 {
		close(b.stopped)
	}
}
