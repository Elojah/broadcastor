package subscriber

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Sentinels matched by the error types below with errors.Is, which does not need T.
var (
	ErrClosed  = errors.New("subscriber closed")
	ErrTimeout = errors.New("timeout")
	ErrDropped = errors.New("message dropped")
	ErrPanic   = errors.New("panic in handle")
	ErrStore   = errors.New("store failed")
	ErrEvicted = errors.New("subscriber evicted")
	ErrExpired = errors.New("message expired")
)

// HandleError is what middleware.WrapError makes of handle's errors.
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

// PanicError is what middleware.Recover makes of a panic. It matches ErrPanic, and unwraps to Value when that is an
// error.
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

// ExpiredError is what middleware.MaxAge returns for a message Age old. It matches ErrExpired.
type ExpiredError[T any] struct {
	SubscriberID uuid.UUID
	Message      T
	Age          time.Duration
}

func (e *ExpiredError[T]) Error() string {
	return "subscriber " + e.SubscriberID.String() + ": message expired, " + e.Age.String() + " old"
}

func (e *ExpiredError[T]) Is(target error) bool {
	return target == ErrExpired
}

// TimeoutError is reported when Broadcast gives up on a subscriber: Err is the Broadcast ctx's error, or
// context.DeadlineExceeded for the message's timeout. It matches ErrTimeout, and unwraps to Err.
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

// DroppedError is reported when a non-blocking Broadcast finds the subscriber busy, or an async one hits WithAsyncLimit.
// It matches ErrDropped.
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

// EvictedError replaces the error that evicted the subscriber (WithEvict): Err is the *TimeoutError, the *DroppedError
// or handle's error. It matches ErrEvicted, and unwraps to Err.
type EvictedError[T any] struct {
	SubscriberID uuid.UUID
	Message      T
	Err          error
}

func (e *EvictedError[T]) Error() string {
	return "subscriber " + e.SubscriberID.String() + ": evicted: " + e.Err.Error()
}

func (e *EvictedError[T]) Unwrap() error {
	return e.Err
}

func (e *EvictedError[T]) Is(target error) bool {
	return target == ErrEvicted
}

// ClosedError is reported for a message missed because the subscriber was unsubscribed: by a Broadcast under way, or
// discarded (WithUnsubscribeDiscard, or a SubscribeSeq loop that ended). It matches ErrClosed.
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

// StoreError replaces the error about a lost message the Store failed to store: Err is Put's, Cause the original. It
// matches ErrStore, and unwraps to both.
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
