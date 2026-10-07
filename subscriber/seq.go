package subscriber

import (
	"context"

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
