package broadcastor

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

type Broadcastor[T any] struct {
	subscribers sync.Map // [uuid.UUID]*subscriber[T]
}

func NewBroadcastor[T any]() *Broadcastor[T] {
	return &Broadcastor[T]{}
}

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

func (b *Broadcastor[T]) Unsubscribe(ctx context.Context, id uuid.UUID) error {
	s, ok := b.subscribers.LoadAndDelete(id)
	if !ok {
		return &SubscriberNotFoundError{id: id}
	}

	// Closes the channel now, or once the last Broadcast still sending on it is done.
	s.(*subscriber[T]).release()

	return nil
}

func (b *Broadcastor[T]) Broadcast(ctx context.Context, msg T, options ...MessageOptions[T]) {
	b.subscribers.Range(func(_, value any) bool {
		s := value.(*subscriber[T])

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

		if m.async {
			go s.send(ctx, m)
		} else {
			s.send(ctx, m)
		}

		return true
	})
}
