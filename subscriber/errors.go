package subscriber

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Sentinels matched by the typed errors below, so that errors.Is can tell them apart without knowing T.
var (
	// ErrClosed is matched by *ClosedError.
	ErrClosed = errors.New("subscriber closed")
	// ErrTimeout is matched by *TimeoutError.
	ErrTimeout = errors.New("timeout")
	// ErrDropped is matched by *DroppedError.
	ErrDropped = errors.New("message dropped")
	// ErrPanic is matched by *PanicError.
	ErrPanic = errors.New("panic in handle")
	// ErrStore is matched by *StoreError.
	ErrStore = errors.New("store failed")
)

// HandleError is what middleware.WrapError makes of an error returned by the handler it wraps: the subscriber and the
// message it failed on, and the error it returned.
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

// PanicError is what middleware.Recover makes of a panic in the handler it wraps: the subscriber and the message it
// panicked on, the value it panicked with, and the stack of the goroutine at that point.
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

// DroppedError is what an error handler is given when a message sent with message.WithNonBlocking is dropped, because
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

// ClosedError is what an error handler is given when a subscriber misses a message because it was unsubscribed:
// either Broadcast skips it, because its channel was closed between Broadcast picking it up and sending to it, or it
// took the message but its SubscribeSeq loop ended before yielding it, or it took the message after being unsubscribed
// with WithUnsubscribeDiscard. It matches ErrClosed.
type ClosedError[T any] struct {
	SubscriberID uuid.UUID
	Message      T
}

func (e *ClosedError[T]) Error() string {
	return "subscriber " + e.SubscriberID.String() + ": unsubscribed, message dropped"
}

func (e *ClosedError[T]) Is(target error) bool {
	return target == ErrClosed
}

// StoreError is what an error handler is given about a message the subscriber lost when its Store (WithStore) failed to
// store it: Err is the error Put returned, and Cause the error about the message that the handlers would otherwise have
// been given. It matches ErrStore, and unwraps to both Err and Cause, so errors.Is and errors.As still find Cause.
type StoreError[T any] struct {
	SubscriberID uuid.UUID
	Message      T
	Err          error
	Cause        error
}

func (e *StoreError[T]) Error() string {
	return "subscriber " + e.SubscriberID.String() + ": storing a lost message: " + e.Err.Error() + " (lost: " + e.Cause.Error() + ")"
}

func (e *StoreError[T]) Unwrap() []error {
	return []error{e.Err, e.Cause}
}

func (e *StoreError[T]) Is(target error) bool {
	return target == ErrStore
}
