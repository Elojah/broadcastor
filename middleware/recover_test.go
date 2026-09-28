package middleware_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
)

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
