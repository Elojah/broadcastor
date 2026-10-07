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

// Middlewares wrap handle in order, the first one outermost, across several options, and nil ones are skipped.
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

// A middleware sees handle's error as is, and its own reaches the error handler as is: nothing wraps it unless a
// middleware does.
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

// With middleware.Recover first, a panic in a later middleware is a *PanicError, like one in handle, and the subscriber
// goes on.
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

// middleware.Retry calls handle again in the subscriber's goroutine, so meanwhile the subscriber takes nothing, and the
// next Broadcast waits.
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

// Cancelling the Subscribe ctx during a middleware.Retry wait unsubscribes and ends the wait: the last error is
// reported at once, and the goroutine ends.
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

// Sharing a store, middleware.History and subscriber.WithDeadLetters put each message in it once: those handled with a
// nil Err, the others with their error.
func TestSubscriberWithMiddleware_History(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		history := &recordStore{}
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			switch msg {
			case 2:
				return handleError(msg)
			case 3:
				panic(handleError(msg))
			default:
				return nil
			}
		},
			subscriber.WithMiddleware(middleware.Recover[int](), middleware.History[int](history)),
			subscriber.WithDeadLetters[int](history),
		)

		for msg := 1; msg <= 4; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		unsubscribe(t, b, id)
		synctest.Wait()

		records := history.messages()
		if len(records) != 4 {
			t.Fatalf("store got %v, want a single record for each of messages 1 to 4", records)
		}
		// All from the subscriber's goroutine, so in message order.
		for i, want := range []error{nil, handleError(2), subscriber.ErrPanic, nil} {
			msg := i + 1
			if r := records[i]; r.SubscriberID != id || r.Message != msg || !errors.Is(r.Err, want) {
				t.Errorf("record %d is for subscriber %s and message %d with error %v, want %s, %d and %v",
					i, r.SubscriberID, r.Message, r.Err, id, msg, want)
			}
		}
	})
}

// middleware.MaxAge does not hand handle a message that waited too long: the error handlers get a
// *subscriber.ExpiredError, the dead letters the message, and it counts as Failed.
func TestSubscriberWithMiddleware_MaxAge(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		// Every message is made now.
		made := time.Now()
		handled := &recorder[int]{}
		errs := &recorder[error]{}
		lost := &recordStore{}
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			handled.record(msg)
			time.Sleep(2 * time.Second)

			return nil
		},
			subscriber.WithBuffer[int](1),
			subscriber.WithMiddleware(middleware.MaxAge(time.Second, func(int) time.Time { return made })),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) { errs.record(err) }),
			subscriber.WithDeadLetters[int](lost),
		)

		b.Broadcast(t.Context(), 1) // handle takes 2s on 1, so 2 waits 2s in the buffer
		b.Broadcast(t.Context(), 2)
		time.Sleep(2 * time.Second) // until handle is done with 1
		synctest.Wait()

		if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		got := errs.messages()
		var expired *subscriber.ExpiredError[int]
		if len(got) != 1 || !errors.As(got[0], &expired) || expired.SubscriberID != id || expired.Message != 2 ||
			expired.Age != 2*time.Second {
			t.Errorf("error handler got %v, want a *subscriber.ExpiredError for subscriber %s and message 2, 2s old", got, id)
		}
		if records := lost.messages(); len(records) != 1 || records[0].Message != 2 || !errors.Is(records[0].Err, subscriber.ErrExpired) {
			t.Errorf("dead letters got %v, want message 2 with a *subscriber.ExpiredError", records)
		}
		checkStats(t, b, id, subscriber.Stats{Buffer: 1, Delivered: 2, Handled: 1, Failed: 1, HandleTime: 2 * time.Second})

		unsubscribe(t, b, id)
		synctest.Wait()
	})
}

// Middlewares wrap a SubscribeSeq loop body like handle: they get the error passed to fail, and Retry yields the
// message again. Once the loop has ended, the message is a *subscriber.ClosedError, which Retry does not retry.
func TestSubscriberWithMiddleware_SubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		results := &recorder[string]{}
		spy := func(next subscriber.Handler[int]) subscriber.Handler[int] {
			return func(ctx context.Context, id uuid.UUID, msg int) error {
				err := next(ctx, id, msg)
				result := "ok"
				switch {
				case errors.Is(err, subscriber.ErrClosed):
					result = "closed"
				case err != nil:
					result = err.Error()
				}
				results.record(fmt.Sprintf("%d: %s", msg, result))

				return err
			}
		}
		errs := &recorder[error]{}
		_, seq := subscribeSeq(t, b,
			subscriber.WithMiddleware(middleware.Retry[int](middleware.RetryPolicy{Attempts: 3}), spy),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) { errs.record(err) }))

		go func() {
			b.Broadcast(t.Context(), 1)
			b.Broadcast(t.Context(), 2)
		}()
		var got []int
		for msg, fail := range seq {
			got = append(got, msg)
			// 1 fails once, then succeeds. 2 fails, and the loop breaks before Retry can yield it again.
			if len(got) == 1 || msg == 2 {
				fail(handleError(msg))
			}
			if msg == 2 {
				break
			}
		}
		synctest.Wait()

		if want := []int{1, 1, 2}; !slices.Equal(got, want) {
			t.Errorf("loop got %v, want %v", got, want)
		}
		want := []string{"1: handling 1 failed", "1: ok", "2: handling 2 failed", "2: closed"}
		if got := results.messages(); !slices.Equal(got, want) {
			t.Errorf("middleware got %q, want %q", got, want)
		}
		if got := errs.messages(); len(got) != 1 || !errors.Is(got[0], subscriber.ErrClosed) {
			t.Errorf("error handler got %v, want a single *subscriber.ClosedError", got)
		}
	})
}

// Recover cannot catch a panic in a SubscribeSeq loop body, which reaches the caller: the message is reported as a
// *subscriber.ClosedError, not a *subscriber.PanicError.
func TestSubscriberWithMiddleware_SubscribeSeqPanic(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		closed := &recorder[int]{}
		_, seq := subscribeSeq(t, b, subscriber.WithMiddleware(middleware.Recover[int]()),
			subscriber.WithErrorHandler[int](recordClosed(t, closed)))

		go b.Broadcast(t.Context(), 1)
		p := recovered(func() {
			for msg := range seq {
				panic(handleError(msg))
			}
		})
		synctest.Wait()

		if p != handleError(1) {
			t.Errorf("loop panicked with %v, want %v", p, handleError(1))
		}
		if got, want := closed.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("error handler got *subscriber.ClosedError for messages %v, want %v", got, want)
		}
	})
}
