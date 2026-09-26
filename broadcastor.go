package broadcastor

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// Broadcastor hands every message passed to Broadcast to each of its subscribers. Its zero value is not usable: create
// one with NewBroadcastor. It is safe for concurrent use, including from within a subscriber's handle.
type Broadcastor[T any] struct {
	subscribers sync.Map // [uuid.UUID]*subscriber[T]
}

// NewBroadcastor returns a Broadcastor with no subscribers.
func NewBroadcastor[T any]() *Broadcastor[T] {
	return &Broadcastor[T]{}
}

// Subscribe adds a subscriber and returns its ID, for Unsubscribe. The subscriber gets its own goroutine, which calls
// handle with ctx for every message it is sent, one at a time and in the order it takes them, until it is unsubscribed
// and has processed everything it took. Cancelling ctx does not unsubscribe it. The only error is a failure to
// generate the ID.
func (b *Broadcastor[T]) Subscribe(ctx context.Context, handle func(ctx context.Context, msg T) error, options ...SubscriberOption[T]) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}

	s := &subscriber[T]{id: id, ch: make(chan message[T])}
	for _, option := range options {
		option(s)
	}
	s.refs.Store(1)
	go s.consume(ctx, handle)

	b.subscribers.Store(id, s)

	return id, nil
}

// Unsubscribe removes the subscriber with the given ID, or returns a *SubscriberNotFoundError if there is none. It
// never waits: the subscriber may still get messages after Unsubscribe returns, from its buffer or from a Broadcast
// that was already sending to it, and its goroutine ends once it has processed them. So it is safe to call from the
// subscriber's own handle. ctx is unused.
func (b *Broadcastor[T]) Unsubscribe(ctx context.Context, id uuid.UUID) error {
	v, _ := b.subscribers.LoadAndDelete(id)
	s, ok := v.(*subscriber[T])
	if !ok {
		return &SubscriberNotFoundError{SubscriberID: id}
	}

	// Closes the channel now, or once the last Broadcast still sending on it is done.
	s.release()

	return nil
}

// Broadcast hands msg to every subscriber, and returns how many it handed it to: the subscribers that took it, plus
// those that an async send was started for, which may still miss it.
//
// By default, Broadcast goes through the subscribers one at a time, and waits for each to take the message. A
// subscriber only takes its next message once handle has returned, unless it has room in its buffer, so a slow
// subscriber holds up Broadcast and every subscriber after it. Broadcast stops waiting once ctx is done, or once the
// message's timeout runs out, counted separately for each subscriber: that subscriber misses the message, and so may
// every later one once ctx is done. WithMessageAsync and WithMessageNonBlocking change how Broadcast waits.
//
// Every subscriber that misses the message has its error handlers given the reason, with ctx: a *TimeoutError, a
// *DroppedError, or a *SubscriberClosedError when it was unsubscribed since Broadcast picked it up.
func (b *Broadcastor[T]) Broadcast(ctx context.Context, msg T, options ...MessageOptions[T]) int {
	var n int
	b.subscribers.Range(func(_, value any) bool {
		s, ok := value.(*subscriber[T])
		if !ok {
			return true
		}

		// The subscriber's defaults first, so that the Broadcast's own options override them.
		m := s.defaults
		m.value = msg
		for _, option := range options {
			option(&m)
		}

		if !s.acquire() {
			s.report(ctx, m, &SubscriberClosedError[T]{SubscriberID: s.id, Message: msg})

			return true // unsubscribed and closed since Range picked it up
		}

		if m.delivery == deliveryAsync {
			go s.send(ctx, m)
			n++
		} else if s.send(ctx, m) {
			n++
		}

		return true
	})

	return n
}
