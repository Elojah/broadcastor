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

// Broadcastor hands every message passed to Broadcast to each of its subscribers. Its zero value is not usable: create
// one with NewBroadcastor. It is safe for concurrent use, including from within a subscriber's handle.
type Broadcastor[T any] struct {
	subscribers sync.Map // [uuid.UUID]*subscriber.Subscriber[T]

	// gate is entered by add while it stores a subscriber, and closed by Close before it goes through them, so every
	// subscriber is either refused or stored before Close goes through them.
	gate gate.Gate
}

// NewBroadcastor returns a Broadcastor with no subscribers.
func NewBroadcastor[T any]() *Broadcastor[T] {
	return &Broadcastor[T]{}
}

// Subscribe adds a subscriber and returns its ID, for Unsubscribe. The subscriber gets its own goroutine, which calls
// handle with ctx and that same ID for every message it is sent, one at a time and in the order it takes them, until it
// is unsubscribed and has processed everything it took. handle can then unsubscribe its own subscriber with the ID it
// is given. Cancelling ctx does not unsubscribe it, unless subscriber.WithAutoUnsubscribe is given. It returns
// ErrClosed once Close has been called, and otherwise fails only to generate the ID.
func (b *Broadcastor[T]) Subscribe(ctx context.Context, handle func(ctx context.Context, id uuid.UUID, msg T) error, options ...subscriber.Option[T]) (uuid.UUID, error) {
	s, err := b.add(ctx, options)
	if err != nil {
		return uuid.Nil, err
	}
	go s.Consume(ctx, handle)

	return s.ID(), nil
}

// SubscribeSeq adds a subscriber like Subscribe, but instead of calling a handle function from a goroutine of its own,
// it returns its ID and an iterator over the messages it takes:
//
//	_, seq, err := b.SubscribeSeq(ctx)
//	...
//	for msg := range seq {
//		...
//	}
//
// The loop body takes the place of handle, in the goroutine that ranges over seq. The subscriber takes its next message
// only once the body is done with the previous one, and takes nothing before the loop starts, so until then Broadcast
// waits for it as for a busy handle.
//
// The loop ends when it breaks (or returns, or panics), when ctx is done, or once the subscriber was unsubscribed and has
// yielded every message it took, or right away if it was unsubscribed with subscriber.WithUnsubscribeDiscard. Ending
// the loop unsubscribes the subscriber. Every message it took but did not yield, from its buffer or from a Broadcast
// that was already sending to it, is reported to its error handlers as a *subscriber.ClosedError with ctx, from a
// goroutine of its own. seq can be ranged over only once: any other range yields nothing.
//
// subscriber.WithMiddleware has no effect, since the loop body runs in the caller's goroutine. It returns ErrClosed
// once Close has been called, and otherwise fails only to generate the ID.
func (b *Broadcastor[T]) SubscribeSeq(ctx context.Context, options ...subscriber.Option[T]) (uuid.UUID, iter.Seq[T], error) {
	s, err := b.add(ctx, options)
	if err != nil {
		return uuid.Nil, nil, err
	}
	id := s.ID()

	return id, s.Seq(ctx, func() { b.remove(id) }), nil
}

// Unsubscribe removes the subscriber with the given ID, or returns a *SubscriberNotFoundError if there is none. It
// never waits: the subscriber may still get messages after Unsubscribe returns, from its buffer or from a Broadcast
// that was already sending to it, and its goroutine ends once it has processed them, or reported them with
// subscriber.WithUnsubscribeDiscard. So it is safe to call from the subscriber's own handle. options override the
// subscriber's subscriber.WithDefaultUnsubscribeOptions. ctx is unused.
func (b *Broadcastor[T]) Unsubscribe(ctx context.Context, id uuid.UUID, options ...subscriber.UnsubscribeOption) error {
	if !b.remove(id, options...) {
		return &SubscriberNotFoundError{SubscriberID: id}
	}

	return nil
}

// Close unsubscribes every subscriber, as Unsubscribe does, and makes every later Subscribe and SubscribeSeq return
// ErrClosed. Once it returns, Broadcast has no subscriber left to hand messages to, and returns 0.
//
// Like Unsubscribe, it never waits: subscribers may still get messages after Close returns, from their buffer or from
// a Broadcast that was already sending to them, and their goroutines end once they have processed them, or reported
// them if their subscriber.WithDefaultUnsubscribeOptions include subscriber.WithUnsubscribeDiscard. So it is safe to
// call from a subscriber's handle, and concurrently with any other method, including Close itself. Every call after the
// first returns ErrClosed, once it has made sure that no subscriber is left.
func (b *Broadcastor[T]) Close() error {
	closed := b.gate.Close()

	// Also on a later call, which may return before the first is done, so that no subscriber is left once any Close
	// returns. remove makes sure each subscriber's reference is dropped once.
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

// Broadcast hands msg to every subscriber, and returns how many it handed it to: the subscribers that took it, plus
// those that an async send was started for, which may still miss it.
//
// By default, Broadcast goes through the subscribers one at a time, and waits for each to take the message. A
// subscriber only takes its next message once handle has returned, unless it has room in its buffer, so a slow
// subscriber holds up Broadcast and every subscriber after it. Broadcast stops waiting once ctx is done, or once the
// message's timeout runs out, counted separately for each subscriber: that subscriber misses the message, and so may
// every later one once ctx is done. message.WithParallel, message.WithAsync and message.WithNonBlocking change how
// Broadcast waits.
//
// Every subscriber that misses the message has its error handlers given the reason, with ctx: a
// *subscriber.TimeoutError, a *subscriber.DroppedError, or a *subscriber.ClosedError when it was unsubscribed since
// Broadcast picked it up.
func (b *Broadcastor[T]) Broadcast(ctx context.Context, msg T, options ...message.Option[T]) int {
	var (
		n int
		// pending holds a channel for each parallel send, which yields whether the subscriber took the message.
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

	// Each gives up once ctx is done or its timeout runs out, like a sync send, so this waits for no other Broadcast.
	for _, taken := range pending {
		if <-taken {
			n++
		}
	}

	return n
}

// add creates a subscriber with the given options and adds it to b, which then sends it every message, or returns
// ErrClosed once b is closed. Nothing reads its channel yet. With subscriber.WithAutoUnsubscribe, it removes the
// subscriber once ctx is done.
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

	// Before the subscriber is stored, so that whoever removes it stops the watch.
	autoUnsubscribe := s.AutoUnsubscribe(ctx, func() { b.remove(id) })
	b.subscribers.Store(id, s)
	// ctx may have been done early enough for the watch to run before the subscriber was stored, and find nothing.
	if autoUnsubscribe && ctx.Err() != nil {
		b.remove(id)
	}

	return s, nil
}

// remove removes the subscriber with the given ID from b and drops the subscription's reference, or reports false if
// there is none. Only whoever deletes it from the map drops that reference, so an Unsubscribe and a Close racing on
// the same subscriber drop it once between them. options apply on top of the subscriber's unsubscribe defaults.
func (b *Broadcastor[T]) remove(id uuid.UUID, options ...subscriber.UnsubscribeOption) bool {
	v, _ := b.subscribers.LoadAndDelete(id)
	s, ok := v.(*subscriber.Subscriber[T])
	if !ok {
		return false
	}
	s.Unsubscribe(options...)

	return true
}
