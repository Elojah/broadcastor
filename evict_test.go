package broadcastor_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

// The first error evict returns true for unsubscribes the subscriber: it is reported as a *subscriber.EvictedError
// wrapping the *subscriber.TimeoutError, and Broadcast stops waiting for it.
func TestSubscriberWithEvict(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		id := subscribe(t, b, stuck.handle, subscriber.WithTimeout[int](time.Second), subscriber.WithEvict[int](isTimeout, nil),
			subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		// stuck holds on to 1, so 2 times out and evicts it.
		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		if got, want := losses.messages(), []string{"2 evicted, timed out"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}

		start := time.Now()
		if n := b.Broadcast(t.Context(), 3); n != 0 {
			t.Errorf("Broadcast once evicted handed the message to %d subscribers, want 0", n)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Broadcast once evicted returned after %v, want no wait", elapsed)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once evicted = %v, want a *SubscriberNotFoundError", err)
		}
		if stats := b.Stats(); len(stats) != 0 {
			t.Errorf("Stats once evicted are for %d subscribers, want none", len(stats))
		}

		stuck.release()
		synctest.Wait()
		if got, want := stuck.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

// onEvict gets the *subscriber.EvictedError after the error handler, with the same ctx: here the evicting message's.
func TestSubscriberWithEvict_OnEvict(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		events := &recorder[string]{}
		record := func(name string, ctx context.Context, err error) {
			v, _ := ctx.Value(key{}).(string)
			events.record(fmt.Sprintf("%s: %s, ctx %q", name, describeLoss(t, err), v))
		}
		ids := &recorder[uuid.UUID]{}
		id := subscribe(t, b, stuck.handle, subscriber.WithTimeout[int](time.Second),
			subscriber.WithEvict(isTimeout, func(ctx context.Context, evicted *subscriber.EvictedError[int]) {
				ids.record(evicted.SubscriberID)
				record("onEvict", ctx, evicted)
			}),
			subscriber.WithErrorHandler[int](func(ctx context.Context, err error) { record("error handler", ctx, err) }))

		// stuck holds on to 1, so 2 times out and evicts it.
		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2, message.WithContext[int](context.WithValue(t.Context(), key{}, "message")))
		want := []string{
			`error handler: 2 evicted, timed out, ctx "message"`,
			`onEvict: 2 evicted, timed out, ctx "message"`,
		}
		if got := events.messages(); !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
		if got, want := ids.messages(), []uuid.UUID{id}; !slices.Equal(got, want) {
			t.Errorf("onEvict got subscriber IDs %v, want %v", got, want)
		}

		stuck.release()
		synctest.Wait()
	})
}

// Only the errors evict picks evict: here drops, not timeouts. A detached subscriber (subscriber.WithDetachedContext)
// is evicted like any other.
func TestSubscriberWithEvict_Dropped(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		evict := &recorder[string]{}
		id := subscribe(t, b, stuck.handle, subscriber.WithEvict[int](func(err error) bool {
			evict.record(describeLoss(t, err))

			return errors.Is(err, subscriber.ErrDropped)
		}, nil), subscriber.WithDetachedContext[int](), subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		b.Broadcast(t.Context(), 1) // stuck holds on to 1, so 2 times out and 3 is dropped
		b.Broadcast(t.Context(), 2, message.WithTimeout[int](time.Second))
		b.Broadcast(t.Context(), 3, message.WithNonBlocking[int]())
		if got, want := evict.messages(), []string{"2 timed out", "3 dropped"}; !slices.Equal(got, want) {
			t.Errorf("evict got %q, want %q", got, want)
		}
		if got, want := losses.messages(), []string{"2 timed out", "3 evicted, dropped"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once evicted = %v, want a *SubscriberNotFoundError", err)
		}

		stuck.release()
		synctest.Wait()
	})
}

// handle's error evicts too, from the subscriber's goroutine, when evict picks it: here 2's, not 1's. What the
// subscriber took after it is reported, not handled.
func TestSubscriberWithEvict_HandleError(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		losses := &recorder[string]{}
		onEvicted := &recorder[string]{}
		proceed := make(chan struct{})
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			<-proceed
			handled.record(msg)

			return handleError(msg)
		}, subscriber.WithBuffer[int](3),
			subscriber.WithEvict(func(err error) bool { return errors.Is(err, handleError(2)) },
				func(_ context.Context, evicted *subscriber.EvictedError[int]) {
					onEvicted.record(describeLoss(t, evicted))
				}),
			subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		close(proceed)
		synctest.Wait()

		if got, want := losses.messages(), []string{"1 failed", "2 evicted, failed", "3 closed"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
		if got, want := onEvicted.messages(), []string{"2 evicted, failed"}; !slices.Equal(got, want) {
			t.Errorf("onEvict got %q, want %q", got, want)
		}
		if got, want := handled.messages(), []int{1, 2}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once evicted = %v, want a *SubscriberNotFoundError", err)
		}
	})
}

// To evict after several errors, a middleware counts them and returns an error evict picks: here two failures in a
// row, which a message handled resets.
func TestSubscriberWithEvict_Middleware(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		losses := &recorder[string]{}
		proceed := make(chan struct{})
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			<-proceed
			handled.record(msg)
			if msg == 2 {
				return nil
			}

			return handleError(msg)
		}, subscriber.WithBuffer[int](5), subscriber.WithMiddleware(failedInARow(2)),
			subscriber.WithEvict[int](func(err error) bool { return errors.Is(err, errInARow) }, nil),
			subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		for msg := 1; msg <= 5; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		close(proceed)
		synctest.Wait()

		if got, want := losses.messages(), []string{"1 failed", "3 failed", "4 evicted, failed", "5 closed"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
		if got, want := handled.messages(), []int{1, 2, 3, 4}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once evicted = %v, want a *SubscriberNotFoundError", err)
		}
	})
}

// Eviction discards, even when the subscriber delivers by default: its buffer is reported, not handled.
func TestSubscriberWithEvict_Discard(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		subscribe(t, b, stuck.handle, subscriber.WithBuffer[int](1), subscriber.WithTimeout[int](time.Second),
			subscriber.WithEvict[int](isTimeout, nil), subscriber.WithErrorHandler[int](recordLosses(t, losses)),
			subscriber.WithUnsubscribeOptions[int](subscriber.WithUnsubscribeDeliver()))

		// stuck holds on to 1, and 2 fills its buffer, so 3 times out and evicts it.
		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		stuck.release()
		synctest.Wait()

		want := []string{"3 evicted, timed out", "2 closed"}
		if got := losses.messages(); !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
		if got, want := stuck.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

// Evicting a subscriber frees a Broadcast waiting on it, which reports its message as a *subscriber.ClosedError.
func TestSubscriberWithEvict_FreesBroadcast(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		subscribe(t, b, stuck.handle, subscriber.WithEvict[int](isTimeout, nil),
			subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		b.Broadcast(t.Context(), 1) // stuck holds on to 1, and the Broadcast of 2 waits with no timeout
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Broadcast(t.Context(), 2)
		}()
		synctest.Wait()

		start := time.Now()
		b.Broadcast(t.Context(), 3, message.WithTimeout[int](time.Second)) // times out, and evicts stuck
		waitClosed(t, done, "Broadcast waiting on an evicted subscriber")
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("Broadcast waiting on an evicted subscriber returned after %v, want %v, when it was evicted", elapsed, time.Second)
		}
		stuck.release()
		synctest.Wait()

		// Reported from each Broadcast's goroutine, in either order.
		want := []string{"2 closed", "3 evicted, timed out"}
		if got := slices.Sorted(slices.Values(losses.messages())); !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
	})
}

// Simultaneous losses evict once: exactly one is reported as a *subscriber.EvictedError and passed to onEvict.
func TestSubscriberWithEvict_Once(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		onEvicted := &recorder[string]{}
		subscribe(t, b, stuck.handle, subscriber.WithTimeout[int](time.Second),
			subscriber.WithEvict(func(error) bool { return true }, func(_ context.Context, evicted *subscriber.EvictedError[int]) {
				onEvicted.record(describeLoss(t, evicted))
			}),
			subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		b.Broadcast(t.Context(), 1) // stuck holds on to 1, so every other message times out at the same time
		const broadcasters = 4
		var wg sync.WaitGroup
		for msg := 2; msg < 2+broadcasters; msg++ {
			wg.Go(func() { b.Broadcast(t.Context(), msg) })
		}
		waitGroup(t, &wg, "Broadcasts to a stuck subscriber")
		stuck.release()
		synctest.Wait()

		got := losses.messages()
		evictions := 0
		for _, loss := range got {
			switch {
			case strings.HasSuffix(loss, " evicted, timed out"):
				evictions++
			case strings.HasSuffix(loss, " timed out"), strings.HasSuffix(loss, " closed"):
			default:
				t.Errorf("error handler got %q, want a timeout or, once evicted, a *subscriber.ClosedError", loss)
			}
		}
		if len(got) != broadcasters || evictions != 1 {
			t.Errorf("error handler got %q, want %d losses, one of them evicting", got, broadcasters)
		}
		if evicting := slices.DeleteFunc(got, func(loss string) bool { return !strings.Contains(loss, "evicted") }); !slices.Equal(onEvicted.messages(), evicting) {
			t.Errorf("onEvict got %q, want the evicting loss, %q", onEvicted.messages(), evicting)
		}
	})
}

// A SubscribeSeq subscriber is evicted too, even before its loop starts, which then yields nothing, and what it took is
// reported.
func TestSubscriberWithEvict_SubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		losses := &recorder[string]{}
		id, seq := subscribeSeq(t, b, subscriber.WithBuffer[int](1), subscriber.WithTimeout[int](time.Second),
			subscriber.WithEvict[int](isTimeout, nil), subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		// Nothing reads before the loop starts: 1 fills the buffer, so 2 times out and evicts the subscriber.
		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		for msg := range seq {
			t.Errorf("loop got %d, want nothing once evicted", msg)
		}
		synctest.Wait()

		if got, want := losses.messages(), []string{"2 evicted, timed out", "1 closed"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once evicted = %v, want a *SubscriberNotFoundError", err)
		}
	})
}

// The error a SubscribeSeq loop body passes to fail evicts like handle's, and ends the loop.
func TestSubscriberWithEvict_SubscribeSeqFail(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		losses := &recorder[string]{}
		id, seq := subscribeSeq(t, b, subscriber.WithBuffer[int](2),
			subscriber.WithEvict[int](func(err error) bool { return errors.Is(err, handleError(1)) }, nil),
			subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		var got []int
		for msg, fail := range seq {
			got = append(got, msg)
			fail(handleError(msg))
		}
		synctest.Wait()

		if want := []int{1}; !slices.Equal(got, want) {
			t.Errorf("loop got %v, want %v", got, want)
		}
		if got, want := losses.messages(), []string{"1 evicted, failed", "2 closed"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once evicted = %v, want a *SubscriberNotFoundError", err)
		}
	})
}

// The *subscriber.ClosedError a SubscribeSeq loop that ended without handling its message returns never reaches evict,
// so the loop's own removal is not reported as an eviction: here the body panics.
func TestSubscriberWithEvict_SubscribeSeqPanic(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		losses := &recorder[string]{}
		_, seq := subscribeSeq(t, b, subscriber.WithEvict(func(err error) bool {
			t.Errorf("evict got %v, want no call", err)

			return true
		}, func(_ context.Context, evicted *subscriber.EvictedError[int]) {
			t.Errorf("onEvict got %v, want no call", evicted)
		}), subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		go b.Broadcast(t.Context(), 1)
		if p := recovered(func() {
			for msg := range seq {
				panic(handleError(msg))
			}
		}); p != handleError(1) {
			t.Errorf("loop panicked with %v, want %v", p, handleError(1))
		}
		synctest.Wait()

		if got, want := losses.messages(), []string{"1 closed"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
	})
}

// A nil evict, the default, or one returning false never evicts, however many messages the subscriber loses, and
// onEvict never runs.
func TestSubscriberWithEvict_Never(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		evict func(error) bool
	}{
		{"Nil", nil},
		{"False", func(error) bool { return false }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := broadcastor.NewBroadcastor[int]()
				stuck := &recorder[int]{hold: make(chan struct{})}
				losses := &recorder[string]{}
				id := subscribe(t, b, stuck.handle, subscriber.WithTimeout[int](time.Second),
					subscriber.WithEvict(tt.evict, func(_ context.Context, evicted *subscriber.EvictedError[int]) {
						t.Errorf("onEvict got %v, want no call", evicted)
					}),
					subscriber.WithErrorHandler[int](recordLosses(t, losses)))

				for msg := 1; msg <= 4; msg++ {
					b.Broadcast(t.Context(), msg) // stuck holds on to 1, so the others time out
				}
				if got, want := losses.messages(), []string{"2 timed out", "3 timed out", "4 timed out"}; !slices.Equal(got, want) {
					t.Errorf("error handler got %q, want %q", got, want)
				}

				unsubscribe(t, b, id)
				stuck.release()
				synctest.Wait()
			})
		})
	}
}

// errInARow is what failedInARow returns.
var errInARow = errors.New("failed in a row")

// failedInARow returns errInARow, wrapping handle's error, once handle has failed n times in a row. It needs no lock:
// the subscriber's goroutine calls it, one message at a time.
func failedInARow(n int) subscriber.Middleware[int] {
	return func(next subscriber.Handler[int]) subscriber.Handler[int] {
		failures := 0

		return func(ctx context.Context, id uuid.UUID, msg int) error {
			err := next(ctx, id, msg)
			if err == nil {
				failures = 0

				return nil
			}
			if failures++; failures >= n {
				return fmt.Errorf("%w: %w", errInARow, err)
			}

			return err
		}
	}
}

// isTimeout is an evict for timeouts.
func isTimeout(err error) bool {
	return errors.Is(err, subscriber.ErrTimeout)
}

// recordLosses returns an error handler that records each error it is given into r, as describeLoss describes it.
func recordLosses(t *testing.T, r *recorder[string]) func(context.Context, error) {
	t.Helper()

	return func(_ context.Context, err error) {
		r.record(describeLoss(t, err))
	}
}

// describeLoss describes err as "<message> timed out", "dropped", "failed" (a handleError) or "closed", with "evicted, "
// before all but closed for a *subscriber.EvictedError, and fails the test on any other error.
func describeLoss(t *testing.T, err error) string {
	t.Helper()
	var (
		evicted *subscriber.EvictedError[int]
		timeout *subscriber.TimeoutError[int]
		dropped *subscriber.DroppedError[int]
		failed  handleError
		closed  *subscriber.ClosedError[int]
	)
	how := ""
	if errors.As(err, &evicted) && errors.Is(err, subscriber.ErrEvicted) {
		how = "evicted, "
	}
	switch {
	case errors.As(err, &timeout) && errors.Is(err, subscriber.ErrTimeout):
		return fmt.Sprintf("%d %stimed out", timeout.Message, how)
	case errors.As(err, &dropped) && errors.Is(err, subscriber.ErrDropped):
		return fmt.Sprintf("%d %sdropped", dropped.Message, how)
	case errors.As(err, &failed):
		return fmt.Sprintf("%d %sfailed", int(failed), how)
	case errors.As(err, &closed) && errors.Is(err, subscriber.ErrClosed) && how == "":
		return fmt.Sprintf("%d closed", closed.Message)
	default:
		t.Errorf("error handler got %v, want a *TimeoutError, *DroppedError, handleError or *ClosedError, or an *EvictedError wrapping one of the first three", err)

		return err.Error()
	}
}
