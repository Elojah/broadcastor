package middleware_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

var errPut = errors.New("put failed")

// History puts each message the handler succeeds on, with the subscriber, a nil Err, and a ctx with the same values,
// never done. It puts nothing when the handler fails or panics, and returns what the handler returns.
func TestHistory(t *testing.T) {
	t.Parallel()

	type key struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, "handle"))
	cancel()
	id := uuid.New()
	var records []subscriber.Record[int]
	history := store.PutFunc[int](func(ctx context.Context, r subscriber.Record[int]) error {
		if v := ctx.Value(key{}); v != "handle" || ctx.Err() != nil {
			t.Errorf("Put got a ctx with value %v and error %v, want the handler's value and a ctx not done", v, ctx.Err())
		}
		records = append(records, r)

		return nil
	})
	handle := middleware.Recover[int]()(middleware.History[int](history)(failOn))

	if err := handle(ctx, id, 1); !errors.Is(err, subscriber.ErrPanic) {
		t.Errorf("handler that panicked returned %v, want a *PanicError", err)
	}
	if err := handle(ctx, id, 2); !errors.Is(err, errHandle) {
		t.Errorf("handler that failed returned %v, want %v", err, errHandle)
	}
	if err := handle(ctx, id, 3); err != nil {
		t.Errorf("handler that succeeded returned %v, want nil", err)
	}

	if len(records) != 1 {
		t.Fatalf("store got %v, want a single record, for message 3", records)
	}
	if r := records[0]; r.SubscriberID != id || r.Message != 3 || r.Err != nil {
		t.Errorf("record is for subscriber %s and message %d with error %v, want %s, 3 and nil", r.SubscriberID, r.Message, r.Err, id)
	}
}

// When Put fails, History returns its error although the handler succeeded, and calls the handler once.
func TestHistory_PutFails(t *testing.T) {
	t.Parallel()

	calls := 0
	history := store.PutFunc[int](func(context.Context, subscriber.Record[int]) error { return errPut })
	handle := middleware.History[int](history)(func(context.Context, uuid.UUID, int) error {
		calls++

		return nil
	})

	if err := handle(t.Context(), uuid.New(), 1); !errors.Is(err, errPut) {
		t.Errorf("History returned %v, want %v", err, errPut)
	}
	if calls != 1 {
		t.Errorf("handler was called %d times, want once", calls)
	}
}
