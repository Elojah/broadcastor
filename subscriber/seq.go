package subscriber

import (
	"context"
	"iter"
	"sync/atomic"

	"github.com/google/uuid"
)

// relay passes each message from Consume's goroutine to a Seq loop, and the body's error back.
type relay[T any] struct {
	// messages is closed once Consume has returned, which ends the loop.
	messages chan T
	errs     chan error

	// stopped is closed once the loop has ended.
	stopped chan struct{}
}

func newRelay[T any]() *relay[T] {
	return &relay[T]{messages: make(chan T), errs: make(chan error), stopped: make(chan struct{})}
}

// handle is Consume's handle for a Seq loop: it returns the error the body passed to fail, or a *ClosedError if the
// loop ended, or panicked, without handling msg.
func (r *relay[T]) handle(_ context.Context, id uuid.UUID, msg T) error {
	select {
	case r.messages <- msg:
	case <-r.stopped:
		return &ClosedError[T]{SubscriberID: id, Message: msg}
	}

	// The loop sends on errs before it ends, unless it ends without yielding msg or its body panics.
	select {
	case err := <-r.errs:
		return err
	case <-r.stopped:
		return &ClosedError[T]{SubscriberID: id, Message: msg}
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
