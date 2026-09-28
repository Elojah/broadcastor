package broadcastor_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
)

var errMiddleware = errors.New("middleware failed")

// Middlewares wrap handle in the order they are given, the first one outermost, across several
// subscriber.WithMiddleware options, and get the ctx passed to Subscribe and the subscriber's ID. A nil middleware is
// skipped.
func TestSubscriberWithMiddleware(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		ctx := context.WithValue(subscribeCtx(t), key{}, "subscribe")
		b := broadcastor.NewBroadcastor[int]()
		calls := &recorder[string]{}
		ids := &recorder[uuid.UUID]{}
		trace := func(name string) subscriber.Middleware[int] {
			return func(next subscriber.Handler[int]) subscriber.Handler[int] {
				return func(ctx context.Context, id uuid.UUID, msg int) error {
					if v := ctx.Value(key{}); v != "subscribe" {
						t.Errorf("middleware %s got a ctx with value %v, want the one passed to Subscribe", name, v)
					}
					ids.record(id)
					calls.record(fmt.Sprintf("%s before %d", name, msg))
					err := next(ctx, id, msg)
					calls.record(fmt.Sprintf("%s after %d", name, msg))

					return err
				}
			}
		}
		id, err := b.Subscribe(ctx, func(_ context.Context, id uuid.UUID, msg int) error {
			ids.record(id)
			calls.record(fmt.Sprintf("handle %d", msg))

			return nil
		},
			subscriber.WithMiddleware(trace("a"), nil, trace("b")),
			subscriber.WithMiddleware(trace("c")),
		)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		b.Broadcast(t.Context(), 1)
		unsubscribe(t, b, id)
		synctest.Wait()

		want := []string{"a before 1", "b before 1", "c before 1", "handle 1", "c after 1", "b after 1", "a after 1"}
		if got := calls.messages(); !slices.Equal(got, want) {
			t.Errorf("calls were %q, want %q", got, want)
		}
		if got, want := ids.messages(), slices.Repeat([]uuid.UUID{id}, 4); !slices.Equal(got, want) {
			t.Errorf("middlewares and handle got IDs %v, want the subscriber's, %s", got, id)
		}
	})
}

// A middleware that does not call next keeps the message from handle, and nothing is reported when it returns nil.
func TestSubscriberWithMiddleware_SkipsHandle(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		errs := &recorder[error]{}
		evenOnly := func(next subscriber.Handler[int]) subscriber.Handler[int] {
			return func(ctx context.Context, id uuid.UUID, msg int) error {
				if msg%2 == 1 {
					return nil
				}

				return next(ctx, id, msg)
			}
		}
		id := subscribe(t, b, handled.handle, subscriber.WithMiddleware(evenOnly),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
				errs.record(err)
			}))

		for msg := 1; msg <= 4; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		unsubscribe(t, b, id)
		synctest.Wait()

		if got, want := handled.messages(), []int{2, 4}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		if got := errs.messages(); len(got) != 0 {
			t.Errorf("error handler got %v, want nothing", got)
		}
	})
}

// A middleware sees the error handle returns as is, and the error it returns itself reaches the error handler as is
// too: nothing wraps it unless a middleware does.
func TestSubscriberWithMiddleware_Error(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		errs := &recorder[error]{}
		wrap := func(next subscriber.Handler[int]) subscriber.Handler[int] {
			return func(ctx context.Context, id uuid.UUID, msg int) error {
				err := next(ctx, id, msg)
				if want := handleError(msg); !errors.Is(err, want) {
					t.Errorf("middleware got %v, want the error handle returned, %v", err, want)
				}

				return fmt.Errorf("%w: %w", errMiddleware, err)
			}
		}
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			return handleError(msg)
		}, subscriber.WithMiddleware(wrap), subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
			errs.record(err)
		}))

		for msg := 1; msg <= 2; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		unsubscribe(t, b, id)
		synctest.Wait()

		got := errs.messages()
		if len(got) != 2 {
			t.Fatalf("error handler got %v, want an error for each of messages 1 and 2", got)
		}
		for i, err := range got {
			var handleErr *subscriber.HandleError[int]
			if !errors.Is(err, errMiddleware) || !errors.Is(err, handleError(i+1)) || errors.As(err, &handleErr) {
				t.Errorf("error %d = %v, want the middleware's error as is, wrapping %v", i, err, handleError(i+1))
			}
		}
	})
}

// With middleware.Recover first, a panic in a later middleware is reported as a *PanicError for the subscriber, like a
// panic in handle, and the subscriber goes on with the next message.
func TestSubscriberWithMiddleware_Recover(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		errs := &recorder[error]{}
		panicOnOne := func(next subscriber.Handler[int]) subscriber.Handler[int] {
			return func(ctx context.Context, id uuid.UUID, msg int) error {
				if msg == 1 {
					panic(handleError(msg))
				}

				return next(ctx, id, msg)
			}
		}
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			handled.record(msg)
			if msg == 2 {
				panic(handleError(msg))
			}

			return nil
		},
			subscriber.WithMiddleware(middleware.Recover[int](), panicOnOne),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
				errs.record(err)
			}),
		)

		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		unsubscribe(t, b, id)
		synctest.Wait()

		if got, want := handled.messages(), []int{2, 3}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		got := errs.messages()
		if len(got) != 2 {
			t.Fatalf("error handler got %v, want a *PanicError for each of messages 1 and 2", got)
		}
		for i, err := range got {
			msg := i + 1
			var panicErr *subscriber.PanicError[int]
			if !errors.As(err, &panicErr) {
				t.Errorf("error %d = %v, want a *PanicError", i, err)

				continue
			}
			if panicErr.SubscriberID != id || panicErr.Message != msg || panicErr.Value != handleError(msg) {
				t.Errorf("error %d is for subscriber %s and message %d with value %v, want %s, %d and %v",
					i, panicErr.SubscriberID, panicErr.Message, panicErr.Value, id, msg, handleError(msg))
			}
		}
	})
}

// middleware.Retry calls handle again in the subscriber's goroutine, so the subscriber takes no message while it waits,
// and the next Broadcast waits for it.
func TestSubscriberWithMiddleware_Retry(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		failures := &recorder[int]{}
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			// Message 1 fails its first two calls.
			if n := handled.record(msg); msg == 1 && n <= 2 {
				return handleError(msg)
			}

			return nil
		},
			subscriber.WithMiddleware(middleware.Retry[int](middleware.RetryPolicy{Attempts: 3, Delay: time.Second})),
			subscriber.WithErrorHandler[int](recordFailures(t, failures)),
		)

		start := time.Now()
		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		if elapsed := time.Since(start); elapsed != 2*time.Second {
			t.Errorf("second Broadcast returned after %v, want once message 1 was retried twice, after %v", elapsed, 2*time.Second)
		}
		unsubscribe(t, b, id)
		synctest.Wait()

		if got, want := handled.messages(), []int{1, 1, 1, 2}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		if got := failures.messages(); len(got) != 0 {
			t.Errorf("error handler got failures for %v, want none", got)
		}
	})
}

// Cancelling the Subscribe ctx while middleware.Retry waits unsubscribes the subscriber and ends the wait: the error of
// the last call is reported right away, and the subscriber's goroutine ends.
func TestSubscriberWithMiddleware_RetryContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		failures := &recorder[int]{}
		_, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, msg int) error {
			handled.record(msg)

			return handleError(msg)
		},
			subscriber.WithMiddleware(middleware.Retry[int](middleware.RetryPolicy{Attempts: 3, Delay: time.Hour})),
			subscriber.WithErrorHandler[int](recordFailures(t, failures)),
		)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		start := time.Now()
		b.Broadcast(t.Context(), 1)
		synctest.Wait()
		cancel()
		// Waits for the subscriber's goroutine to report, but lets no time pass: a Retry still waiting would not have.
		synctest.Wait()

		if got, want := failures.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("error handler got failures for %v, want %v", got, want)
		}
		if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("%v passed, want none", elapsed)
		}
		if n := b.Broadcast(t.Context(), 2); n != 0 {
			t.Errorf("Broadcast after cancel reached %d subscribers, want 0", n)
		}
	})
}

// Middlewares have no effect on SubscribeSeq, whose loop body takes the place of handle.
func TestSubscriberWithMiddleware_SubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		called := &recorder[int]{}
		spy := func(next subscriber.Handler[int]) subscriber.Handler[int] {
			return func(ctx context.Context, id uuid.UUID, msg int) error {
				called.record(msg)

				return next(ctx, id, msg)
			}
		}
		_, seq := subscribeSeq(t, b, subscriber.WithMiddleware(spy))

		go b.Broadcast(t.Context(), 1)
		var got []int
		for msg := range seq {
			got = append(got, msg)

			break
		}
		synctest.Wait()

		if want := []int{1}; !slices.Equal(got, want) {
			t.Errorf("loop got %v, want %v", got, want)
		}
		if got := called.messages(); len(got) != 0 {
			t.Errorf("middleware got %v, want nothing", got)
		}
	})
}
