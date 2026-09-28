package middleware_test

import (
	"context"
	"errors"
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
