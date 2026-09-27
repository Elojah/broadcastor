package middleware_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
)

var errHandle = errors.New("handle failed")

// failOn is a handler that panics with errHandle on message 1, returns it on message 2, and succeeds otherwise.
func failOn(_ context.Context, _ uuid.UUID, msg int) error {
	switch msg {
	case 1:
		panic(errHandle)
	case 2:
		return errHandle
	default:
		return nil
	}
}

// Recover makes a panic a *PanicError with the subscriber, the message, the value and the stack, and returns what the
// handler returns otherwise.
func TestRecover(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	handle := middleware.Recover[int]()(failOn)

	err := handle(t.Context(), id, 1)
	var panicErr *subscriber.PanicError[int]
	if !errors.As(err, &panicErr) {
		t.Fatalf("handler that panicked returned %v, want a *PanicError", err)
	}
	if panicErr.SubscriberID != id || panicErr.Message != 1 {
		t.Errorf("error is for subscriber %s and message %d, want %s and 1", panicErr.SubscriberID, panicErr.Message, id)
	}
	// errHandle is only reachable through Value, which the error unwraps to.
	if !errors.Is(err, subscriber.ErrPanic) || !errors.Is(err, errHandle) {
		t.Errorf("error %v does not match both ErrPanic and the value the handler panicked with, %v", err, errHandle)
	}
	if !bytes.Contains(panicErr.Stack, []byte("failOn")) {
		t.Errorf("stack does not go through the handler:\n%s", panicErr.Stack)
	}

	if err := handle(t.Context(), id, 2); !errors.Is(err, errHandle) || errors.As(err, &panicErr) {
		t.Errorf("handler that failed returned %v, want %v as is", err, errHandle)
	}
	if err := handle(t.Context(), id, 3); err != nil {
		t.Errorf("handler that succeeded returned %v, want nil", err)
	}
}

// WrapError makes an error a *HandleError with the subscriber and the message, and leaves success alone.
func TestWrapError(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	handle := middleware.WrapError[int]()(failOn)

	err := handle(t.Context(), id, 2)
	var handleErr *subscriber.HandleError[int]
	if !errors.As(err, &handleErr) {
		t.Fatalf("handler that failed returned %v, want a *HandleError", err)
	}
	if handleErr.SubscriberID != id || handleErr.Message != 2 || !errors.Is(err, errHandle) {
		t.Errorf("error is for subscriber %s and message %d wrapping %v, want %s, 2 and %v",
			handleErr.SubscriberID, handleErr.Message, handleErr.Err, id, errHandle)
	}
	if s := err.Error(); !strings.Contains(s, id.String()) || !strings.Contains(s, errHandle.Error()) {
		t.Errorf("error %q does not mention both the subscriber %s and the error from the handler", s, id)
	}

	if err := handle(t.Context(), id, 3); err != nil {
		t.Errorf("handler that succeeded returned %v, want nil", err)
	}
}

// With Recover outside WrapError, the order the package doc recommends, a panic is a *PanicError that is not wrapped in
// a *HandleError, and an error is a *HandleError.
func TestRecover_WrapError(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	handle := middleware.Recover[int]()(middleware.WrapError[int]()(failOn))

	var panicErr *subscriber.PanicError[int]
	var handleErr *subscriber.HandleError[int]
	if err := handle(t.Context(), id, 1); !errors.As(err, &panicErr) || errors.As(err, &handleErr) {
		t.Errorf("handler that panicked returned %v, want a *PanicError not wrapped in a *HandleError", err)
	}
	if err := handle(t.Context(), id, 2); !errors.As(err, &handleErr) {
		t.Errorf("handler that failed returned %v, want a *HandleError", err)
	}
}
