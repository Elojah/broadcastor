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

	"github.com/elojah/broadcastor/v2/message"
	"github.com/elojah/broadcastor/v2/subscriber"
)

// The first error evict returns true for unsubscribes the subscriber: it is reported as a *subscriber.EvictedError
// wrapping the *subscriber.TimeoutError, and Broadcast stops waiting for it.
func TestSubscriberWithEvict(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		stuck := &recorder[int]{hold: make(chan struct{})}
		errs := &recorder[error]{}
		id := subscribe(t, b, stuck.handle, subscriber.WithTimeout[int](time.Second), subscriber.WithEvict[int](isTimeout),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) { errs.record(err) }))

		// stuck holds on to 1, so 2 times out and evicts it.
		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		got := errs.messages()
		var evicted *subscriber.EvictedError[int]
		if len(got) != 1 || !errors.As(got[0], &evicted) || evicted.SubscriberID != id || describeLoss(t, got[0]) != "2 evicted, timed out" {
			t.Errorf("error handler got %v, want 2's eviction of %d, on a timeout", got, id)
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

// Only the errors evict picks evict: here drops, not timeouts. A detached subscriber (subscriber.WithDetachedContext)
// is evicted like any other.
func TestSubscriberWithEvict_Dropped(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		evict := &recorder[string]{}
		id := subscribe(t, b, stuck.handle, subscriber.WithEvict[int](func(err error) bool {
			evict.record(describeLoss(t, err))

			return errors.Is(err, subscriber.ErrDropped)
		}), subscriber.WithDetachedContext[int](), subscriber.WithErrorHandler[int](recordLosses(t, losses)))

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
		b := newBroadcastor[int](t)
		handled := &recorder[int]{}
		losses := &recorder[string]{}
		proceed := make(chan struct{})
		id := subscribe(t, b, func(_ context.Context, _ uint64, msg int) error {
			<-proceed
			handled.record(msg)

			return handleError(msg)
		}, subscriber.WithBuffer[int](3),
			subscriber.WithEvict[int](func(err error) bool { return errors.Is(err, handleError(2)) }),
			subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		close(proceed)
		synctest.Wait()

		if got, want := losses.messages(), []string{"1 failed", "2 evicted, failed", "3 closed"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
		if got, want := handled.messages(), []int{1, 2}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once evicted = %v, want a *SubscriberNotFoundError", err)
		}
	})
}

// Simultaneous losses evict once: exactly one is reported as a *subscriber.EvictedError.
func TestSubscriberWithEvict_Once(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		stuck := &recorder[int]{hold: make(chan struct{})}
		losses := &recorder[string]{}
		const broadcasters = 4
		subscribe(t, b, stuck.handle, subscriber.WithTimeout[int](time.Second),
			subscriber.WithEvict[int](func(error) bool { return true }),
			subscriber.WithErrorHandler[int](recordLosses(t, losses)))

		b.Broadcast(t.Context(), 1) // stuck holds on to 1, so every other message times out at the same time
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
	})
}

// The *subscriber.ClosedError a SubscribeSeq loop that ended without handling its message returns never reaches evict,
// so the loop's own removal is not reported as an eviction: here the body panics.
func TestSubscriberWithEvict_SubscribeSeqPanic(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		losses := &recorder[string]{}
		_, seq := subscribeSeq(t, b, subscriber.WithEvict[int](func(err error) bool {
			t.Errorf("evict got %v, want no call", err)

			return true
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
