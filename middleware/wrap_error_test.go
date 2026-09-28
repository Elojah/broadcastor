package middleware_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
)

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
