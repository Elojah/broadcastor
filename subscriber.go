package broadcastor

import (
	"context"
	"sync/atomic"

	"github.com/google/uuid"
)

type subscriber[T any] struct {
	id uuid.UUID
	ch chan message[T]

	// defaults is what every message sent to this subscriber starts from, before the Broadcast's own options.
	defaults message[T]

	// errorHandler is called with every error handle returns, or nil to discard them.
	errorHandler func(ctx context.Context, err error)

	// refs is 1 for the subscription itself, plus 1 for each Broadcast currently sending on ch. Whoever drops it to 0
	// closes ch, so ch is never closed while a Broadcast can still send on it.
	refs atomic.Int64
}

// acquire takes a reference for a Broadcast, unless ch is already closed.
func (s *subscriber[T]) acquire() bool {
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
func (s *subscriber[T]) release() {
	if s.refs.Add(-1) == 0 {
		close(s.ch)
	}
}

// report passes err to the subscriber's error handler, then to m's, skipping whichever is not set.
func (s *subscriber[T]) report(ctx context.Context, m message[T], err error) {
	if s.errorHandler != nil {
		s.errorHandler(ctx, err)
	}
	if m.errorHandler != nil {
		m.errorHandler(ctx, err)
	}
}

// send hands m to the subscriber, or gives up once ctx is done or m's timeout runs out, then drops the reference the
// caller acquired.
func (s *subscriber[T]) send(ctx context.Context, m message[T]) {
	defer s.release()

	sendCtx := ctx
	if m.timeout > 0 {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(ctx, m.timeout)
		defer cancel()
	}

	select {
	case s.ch <- m:
	case <-sendCtx.Done():
		// ctx, not sendCtx, so that the error handler is not handed a ctx that the timeout alone has ended.
		s.report(ctx, m, &TimeoutError[T]{SubscriberID: s.id, Message: m.value, Err: sendCtx.Err()})
	}
}

func (s *subscriber[T]) consume(ctx context.Context, handle func(ctx context.Context, msg T) error) {
	for m := range s.ch {
		if err := handle(ctx, m.value); err != nil {
			s.report(ctx, m, &HandleError[T]{SubscriberID: s.id, Message: m.value, Err: err})
		}
	}
}
