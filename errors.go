package broadcastor

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Sentinels matched by the typed errors below, so that errors.Is can tell them apart without knowing T.
var (
	// ErrSubscriberNotFound is matched by *SubscriberNotFoundError.
	ErrSubscriberNotFound = errors.New("subscriber not found")
	// ErrSubscriberClosed is matched by *SubscriberClosedError.
	ErrSubscriberClosed = errors.New("subscriber closed")
	// ErrTimeout is matched by *TimeoutError.
	ErrTimeout = errors.New("timeout")
	// ErrDropped is matched by *DroppedError.
	ErrDropped = errors.New("message dropped")
	// ErrPanic is matched by *PanicError.
	ErrPanic = errors.New("panic in handle")
)

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

// PanicError is what an error handler is given when handle panics in a subscriber with WithSubscriberRecover: the
// subscriber and the message it panicked on, the value it panicked with, and the stack of the goroutine at that point.
// It unwraps to Value when Value is an error, and matches ErrPanic.
type PanicError[T any] struct {
	SubscriberID uuid.UUID
	Message      T
	Value        any
	Stack        []byte
}

func (e *PanicError[T]) Error() string {
	return fmt.Sprintf("subscriber %s: panic: %v", e.SubscriberID, e.Value)
}

func (e *PanicError[T]) Unwrap() error {
	err, _ := e.Value.(error)

	return err
}

func (e *PanicError[T]) Is(target error) bool {
	return target == ErrPanic
}

// SubscriberNotFoundError is what Unsubscribe returns for an ID that is not subscribed, or no longer. It matches
// ErrSubscriberNotFound.
type SubscriberNotFoundError struct {
	SubscriberID uuid.UUID
}

func (e *SubscriberNotFoundError) Error() string {
	return "subscriber not found: " + e.SubscriberID.String()
}

func (e *SubscriberNotFoundError) Is(target error) bool {
	return target == ErrSubscriberNotFound
}

// TimeoutError is what an error handler is given when Broadcast gives up handing a message to a subscriber: Err is the
// Broadcast ctx's error, or context.DeadlineExceeded when the message's timeout ran out first. It matches ErrTimeout,
// and unwraps to Err.
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

func (e *TimeoutError[T]) Is(target error) bool {
	return target == ErrTimeout
}

// DroppedError is what an error handler is given when a message sent with WithMessageNonBlocking is dropped, because
// the subscriber could not take it right away. It matches ErrDropped.
type DroppedError[T any] struct {
	SubscriberID uuid.UUID
	Message      T
}

func (e *DroppedError[T]) Error() string {
	return "subscriber " + e.SubscriberID.String() + ": busy, message dropped"
}

func (e *DroppedError[T]) Is(target error) bool {
	return target == ErrDropped
}

// SubscriberClosedError is what an error handler is given when Broadcast skips a subscriber that was unsubscribed, and
// its channel closed, between Broadcast picking it up and sending to it. It matches ErrSubscriberClosed.
type SubscriberClosedError[T any] struct {
	SubscriberID uuid.UUID
	Message      T
}

func (e *SubscriberClosedError[T]) Error() string {
	return "subscriber " + e.SubscriberID.String() + ": channel closed"
}

func (e *SubscriberClosedError[T]) Is(target error) bool {
	return target == ErrSubscriberClosed
}
