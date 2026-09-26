package broadcastor

import "github.com/google/uuid"

// HandleError is what an error handler is given when handle fails: the subscriber and the message it failed on, and
// the error it returned.
type HandleError[T any] struct {
	SubscriberID uuid.UUID
	Message      T
	Err          error
}

func (e *HandleError[T]) Error() string {
	return "subscriber " + e.SubscriberID.String() + ": " + e.Err.Error()
}

func (e *HandleError[T]) Unwrap() error {
	return e.Err
}

type SubscriberNotFoundError struct {
	id uuid.UUID
}

func (e *SubscriberNotFoundError) Error() string {
	return "subscriber not found: " + e.id.String()
}

// TimeoutError is what an error handler is given when Broadcast gives up handing a message to a subscriber: Err is the
// Broadcast ctx's error, or context.DeadlineExceeded when the message's timeout ran out first.
type TimeoutError[T any] struct {
	SubscriberID uuid.UUID
	Message      T
	Err          error
}

func (e *TimeoutError[T]) Error() string {
	return "subscriber " + e.SubscriberID.String() + ": " + e.Err.Error()
}

func (e *TimeoutError[T]) Unwrap() error {
	return e.Err
}

type SubscriberClosedError[T any] struct {
	SubscriberID uuid.UUID
	Message      T
}

func (e *SubscriberClosedError[T]) Error() string {
	return "subscriber " + e.SubscriberID.String() + ": channel closed"
}
