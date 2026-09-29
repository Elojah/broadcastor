package broadcastor

import (
	"context"
	"iter"
	"sync"

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
}

// NewBroadcastor returns an empty Broadcastor.
func NewBroadcastor[T any]() *Broadcastor[T] {
	return &Broadcastor[T]{}
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
// goroutine. Until the loop starts, Broadcast waits for it as for a busy handle.
//
// The loop ends when it breaks, when ctx is done, or once the subscriber is unsubscribed and has yielded what it took.
// Ending unsubscribes it, and reports what it took but did not yield as *subscriber.ClosedError. seq can be ranged over
// once. subscriber.WithMiddleware has no effect.
//
// It returns ErrClosed after Close.
func (b *Broadcastor[T]) SubscribeSeq(ctx context.Context, options ...subscriber.Option[T]) (uuid.UUID, iter.Seq[T], error) {
	s, err := b.add(ctx, options)
	if err != nil {
		return uuid.Nil, nil, err
	}
	id := s.ID()

	return id, s.Seq(func() { b.remove(id) }), nil
}

// Unsubscribe removes the subscriber, or returns a *SubscriberNotFoundError. It never waits, so handle can call it, and
// the subscriber may still process messages it already took. options override its
// subscriber.WithDefaultUnsubscribeOptions. ctx is unused.
func (b *Broadcastor[T]) Unsubscribe(ctx context.Context, id uuid.UUID, options ...subscriber.UnsubscribeOption) error {
	if !b.remove(id, options...) {
		return &SubscriberNotFoundError{SubscriberID: id}
	}

	return nil
}

// Close unsubscribes every subscriber, and makes later Subscribe and SubscribeSeq calls return ErrClosed. Like
// Unsubscribe it never waits, so handle can call it. Every call after the first returns ErrClosed.
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

	return nil
}

// Broadcast hands msg to every subscriber and returns how many took it, counting every async send as taken.
//
// By default it waits for each subscriber in turn, so a slow one holds up those after it. It gives up on a subscriber
// once ctx is done or the message's timeout runs out, and tells its error handlers why. ctx only bounds the wait, and
// reaches neither handle nor the error handlers, although async sends keep using it (see message.WithAsync).
func (b *Broadcastor[T]) Broadcast(ctx context.Context, msg T, options ...message.Option[T]) int {
	var (
		n int
		// One per parallel send, yielding whether the subscriber took the message.
		pending []<-chan bool
	)
	b.subscribers.Range(func(_, value any) bool {
		s, ok := value.(*subscriber.Subscriber[T])
		if !ok {
			return true
		}
		taken, parallel := s.Deliver(ctx, msg, options...)
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

	// Before Store, so whoever removes the subscriber stops the watch.
	ctx = s.ContextLifetime(ctx, func() { b.remove(id) })
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
