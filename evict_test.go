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

// A subscriber that loses n messages in a row is unsubscribed: the nth loss is reported as a *subscriber.EvictedError
// wrapping the *subscriber.TimeoutError, and Broadcast no longer waits for it.
func TestSubscriberWithEvictAfter(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		id := subscribe(t, b, stuck.handle, subscriber.WithTimeout[int](time.Second), subscriber.WithEvictAfter[int](2, nil),
			subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		// stuck holds on to 1, so 2 and 3 time out, and 3 evicts it.
		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		if got, want := losses.messages(), []string{"2 timed out", "3 evicted, timed out"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}

		start := time.Now()
		if n := b.Broadcast(t.Context(), 4); n != 0 {
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

// onEvict gets the *subscriber.EvictedError once the error handler got it, with the same ctx: here the evicting
// message's own.
func TestSubscriberWithEvictAfter_OnEvict(t *testing.T) {
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
			subscriber.WithEvictAfter(2, func(ctx context.Context, evicted *subscriber.EvictedError[int]) {
				ids.record(evicted.SubscriberID)
				record("onEvict", ctx, evicted)
			}),
			subscriber.WithErrorHandler[int](func(ctx context.Context, err error) { record("error handler", ctx, err) }))

		// stuck holds on to 1, so 2 and 3 time out, and 3 evicts it.
		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		b.Broadcast(t.Context(), 3, message.WithContext[int](context.WithValue(t.Context(), key{}, "message")))
		want := []string{
			`error handler: 2 timed out, ctx ""`,
			`error handler: 3 evicted, timed out, ctx "message"`,
			`onEvict: 3 evicted, timed out, ctx "message"`,
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

// Drops count as losses too, and a detached subscriber (subscriber.WithDetachedContext) is evicted like any other.
func TestSubscriberWithEvictAfter_Dropped(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		id := subscribe(t, b, stuck.handle, subscriber.WithEvictAfter[int](2, nil), subscriber.WithDetachedContext[int](),
			subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		b.Broadcast(t.Context(), 1) // stuck holds on to 1, so 2 and 3 are dropped
		b.Broadcast(t.Context(), 2, message.WithNonBlocking[int]())
		b.Broadcast(t.Context(), 3, message.WithNonBlocking[int]())
		if got, want := losses.messages(), []string{"2 dropped", "3 evicted, dropped"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once evicted = %v, want a *SubscriberNotFoundError", err)
		}

		stuck.release()
		synctest.Wait()
	})
}

// A message the subscriber takes starts the count again: 2 and 4 are not two losses in a row.
func TestSubscriberWithEvictAfter_Reset(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		losses := &recorder[string]{}
		proceed := make(chan struct{})
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			handled.record(msg)
			<-proceed

			return nil
		}, subscriber.WithTimeout[int](time.Second), subscriber.WithEvictAfter[int](2, nil),
			subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		b.Broadcast(t.Context(), 1) // handle holds on to 1, so 2 times out
		b.Broadcast(t.Context(), 2)
		proceed <- struct{}{}
		b.Broadcast(t.Context(), 3) // taken, and handle holds on to it, so 4 and 5 time out, and 5 evicts
		b.Broadcast(t.Context(), 4)
		b.Broadcast(t.Context(), 5)
		close(proceed)
		synctest.Wait()

		want := []string{"2 timed out", "4 timed out", "5 evicted, timed out"}
		if got := losses.messages(); !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once evicted = %v, want a *SubscriberNotFoundError", err)
		}
		if got, want := handled.messages(), []int{1, 3}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

// An evicted subscriber is unsubscribed with subscriber.WithUnsubscribeDiscard, even when it delivers by default: what
// is left in its buffer is reported rather than handled.
func TestSubscriberWithEvictAfter_Discard(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		subscribe(t, b, stuck.handle, subscriber.WithBuffer[int](1), subscriber.WithTimeout[int](time.Second),
			subscriber.WithEvictAfter[int](2, nil), subscriber.WithErrorHandler[int](recordLosses(t, losses)),
			subscriber.WithUnsubscribeOptions[int](subscriber.WithUnsubscribeDeliver()))

		// stuck holds on to 1, and 2 fills its buffer, so 3 and 4 time out, and 4 evicts it.
		for msg := 1; msg <= 4; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		stuck.release()
		synctest.Wait()

		want := []string{"3 timed out", "4 evicted, timed out", "2 closed"}
		if got := losses.messages(); !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
		if got, want := stuck.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

// Evicting a subscriber frees a Broadcast waiting on it, which reports its message as a *subscriber.ClosedError.
func TestSubscriberWithEvictAfter_FreesBroadcast(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		subscribe(t, b, stuck.handle, subscriber.WithEvictAfter[int](1, nil),
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

// Losses at once evict the subscriber once: exactly one of them is reported as a *subscriber.EvictedError, and passed
// to onEvict.
func TestSubscriberWithEvictAfter_Once(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		onEvicted := &recorder[string]{}
		subscribe(t, b, stuck.handle, subscriber.WithTimeout[int](time.Second),
			subscriber.WithEvictAfter(1, func(_ context.Context, evicted *subscriber.EvictedError[int]) {
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

// A SubscribeSeq subscriber is evicted too, even before its loop starts: the loop then yields nothing, and what the
// subscriber took is reported.
func TestSubscriberWithEvictAfter_SubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		losses := &recorder[string]{}
		id, seq := subscribeSeq(t, b, subscriber.WithBuffer[int](1), subscriber.WithTimeout[int](time.Second),
			subscriber.WithEvictAfter[int](1, nil), subscriber.WithErrorHandler[int](recordLosses(t, losses)))

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

// With n at 0, the default, a subscriber is never evicted, however many messages it loses, and onEvict never runs.
func TestSubscriberWithEvictAfter_Never(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		id := subscribe(t, b, stuck.handle, subscriber.WithTimeout[int](time.Second),
			subscriber.WithEvictAfter(0, func(_ context.Context, evicted *subscriber.EvictedError[int]) {
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
}

// recordLosses returns an error handler that records each error it is given into r, as describeLoss describes it.
func recordLosses(t *testing.T, r *recorder[string]) func(context.Context, error) {
	t.Helper()

	return func(_ context.Context, err error) {
		r.record(describeLoss(t, err))
	}
}

// describeLoss describes err as "<message> timed out", "dropped" or "closed", with "evicted, " before timed out or
// dropped for a *subscriber.EvictedError, and fails the test on any other error.
func describeLoss(t *testing.T, err error) string {
	t.Helper()
	var (
		evicted *subscriber.EvictedError[int]
		timeout *subscriber.TimeoutError[int]
		dropped *subscriber.DroppedError[int]
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
	case errors.As(err, &closed) && errors.Is(err, subscriber.ErrClosed) && how == "":
		return fmt.Sprintf("%d closed", closed.Message)
	default:
		t.Errorf("error handler got %v, want a *TimeoutError, *DroppedError or *ClosedError, or an *EvictedError wrapping one of the first two", err)

		return err.Error()
	}
}
