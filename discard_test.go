package broadcastor_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/subscriber"
)

// Once unsubscribed with WithUnsubscribeDiscard, the subscriber reports its buffer instead of handling it, and a
// waiting Broadcast reports its message at once.
func TestWithUnsubscribeDiscard(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		ctx := context.WithValue(subscribeCtx(t), key{}, "subscribe")
		b := newBroadcastor[int](t)
		handled := &recorder[int]{hold: make(chan struct{})}
		closed := &recorder[int]{}
		id, err := b.Subscribe(ctx, handled.handle, subscriber.WithBuffer[int](2),
			subscriber.WithErrorHandler[int](func(ctx context.Context, err error) {
				if v := ctx.Value(key{}); v != "subscribe" {
					t.Errorf("error handler got a ctx with value %v, want the one passed to Subscribe", v)
				}
				recordClosed(t, closed)(ctx, err)
			}))
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		// handle holds on to 1, 2 and 3 fill the buffer, and the Broadcast of 4 waits.
		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Broadcast(t.Context(), 4)
		}()
		synctest.Wait()

		if err := b.Unsubscribe(t.Context(), id, subscriber.WithUnsubscribeDiscard()); err != nil {
			t.Fatalf("Unsubscribe: %v", err)
		}
		waitClosed(t, done, "Broadcast to a subscriber unsubscribed with WithUnsubscribeDiscard")
		handled.release()
		synctest.Wait()

		if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		if got, want := closed.messages(), []int{4, 2, 3}; !slices.Equal(got, want) {
			t.Errorf("error handler got *subscriber.ClosedError for messages %v, want %v", got, want)
		}
	})
}

// handle unsubscribes itself with WithUnsubscribeDiscard while a Broadcast waits on it: Unsubscribe must not wait, and
// the message is reported, not handled.
func TestWithUnsubscribeDiscard_Self(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		handled := &recorder[int]{}
		closed := &recorder[int]{}
		proceed := make(chan struct{})
		subscribe(t, b, func(ctx context.Context, id uuid.UUID, msg int) error {
			handled.record(msg)
			<-proceed
			if err := b.Unsubscribe(ctx, id, subscriber.WithUnsubscribeDiscard()); err != nil {
				t.Errorf("Unsubscribe: %v", err)
			}

			return nil
		}, subscriber.WithErrorHandler[int](recordClosed(t, closed)))

		b.Broadcast(t.Context(), 1) // handle is now processing 1 and not reading
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Broadcast(t.Context(), 2)
		}()
		synctest.Wait() // the Broadcast of 2 is waiting on the subscriber
		close(proceed)
		waitClosed(t, done, "Broadcast during which the subscriber unsubscribed itself with WithUnsubscribeDiscard")
		synctest.Wait()

		if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		if got, want := closed.messages(), []int{2}; !slices.Equal(got, want) {
			t.Errorf("error handler got *subscriber.ClosedError for messages %v, want %v", got, want)
		}
	})
}

// A subscriber's default unsubscribe options apply to Close, and an Unsubscribe's own options override them.
func TestSubscriberWithUnsubscribeOptions(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		subscribeHeld := func(closed *recorder[int]) (*recorder[int], uuid.UUID) {
			handled := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, b, handled.handle, subscriber.WithBuffer[int](2),
				subscriber.WithErrorHandler[int](recordClosed(t, closed)),
				subscriber.WithUnsubscribeOptions[int](subscriber.WithUnsubscribeDiscard()))

			return handled, id
		}
		deliveredClosed := &recorder[int]{}
		deliveredHandled, deliveredID := subscribeHeld(deliveredClosed)
		discardedClosed := &recorder[int]{}
		discardedHandled, _ := subscribeHeld(discardedClosed)

		// Each subscriber holds on to 1, with 2 and 3 in its buffer.
		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		if err := b.Unsubscribe(t.Context(), deliveredID, subscriber.WithUnsubscribeDeliver()); err != nil {
			t.Fatalf("Unsubscribe: %v", err)
		}
		if err := b.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		deliveredHandled.release()
		discardedHandled.release()
		synctest.Wait()

		if got, want := deliveredHandled.messages(), []int{1, 2, 3}; !slices.Equal(got, want) {
			t.Errorf("subscriber unsubscribed with WithUnsubscribeDeliver handled %v, want %v", got, want)
		}
		if got := deliveredClosed.messages(); len(got) != 0 {
			t.Errorf("subscriber unsubscribed with WithUnsubscribeDeliver reported %v, want nothing", got)
		}
		if got, want := discardedHandled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("subscriber discarding by default handled %v after Close, want %v", got, want)
		}
		if got, want := discardedClosed.messages(), []int{2, 3}; !slices.Equal(got, want) {
			t.Errorf("subscriber discarding by default reported %v after Close, want %v", got, want)
		}
	})
}

// After WithUnsubscribeDiscard, a SubscribeSeq loop yields nothing more, and everything taken is reported, after the
// message of a waiting Broadcast, which gives up at once.
func TestSubscribeSeq_UnsubscribeDiscard(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		closed := &recorder[int]{}
		id, seq := subscribeSeq(t, b, subscriber.WithBuffer[int](2),
			subscriber.WithErrorHandler[int](recordClosed(t, closed)))

		// Nothing reads before the loop starts: 1 and 2 fill the buffer, and the Broadcast of 3 waits.
		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Broadcast(t.Context(), 3)
		}()
		synctest.Wait()

		if err := b.Unsubscribe(t.Context(), id, subscriber.WithUnsubscribeDiscard()); err != nil {
			t.Fatalf("Unsubscribe: %v", err)
		}
		waitClosed(t, done, "Broadcast to a subscriber unsubscribed with WithUnsubscribeDiscard")
		for msg := range seq {
			t.Errorf("loop got %d, want nothing", msg)
		}
		synctest.Wait()

		if got, want := closed.messages(), []int{3, 1, 2}; !slices.Equal(got, want) {
			t.Errorf("error handler got *subscriber.ClosedError for messages %v, want %v", got, want)
		}
	})
}

// A loop body unsubscribing itself with WithUnsubscribeDiscard ends the loop without a break, and its buffer is
// reported.
func TestSubscribeSeq_UnsubscribeDiscardSelf(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		closed := &recorder[int]{}
		id, seq := subscribeSeq(t, b, subscriber.WithBuffer[int](2),
			subscriber.WithErrorHandler[int](recordClosed(t, closed)))

		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		var yielded []int
		for msg := range seq {
			yielded = append(yielded, msg)
			if err := b.Unsubscribe(t.Context(), id, subscriber.WithUnsubscribeDiscard()); err != nil {
				t.Errorf("Unsubscribe: %v", err)
			}
		}
		synctest.Wait()

		if want := []int{1}; !slices.Equal(yielded, want) {
			t.Errorf("loop got %v, want %v", yielded, want)
		}
		if got, want := closed.messages(), []int{2}; !slices.Equal(got, want) {
			t.Errorf("error handler got *subscriber.ClosedError for messages %v, want %v", got, want)
		}
	})
}

// recordClosed returns an error handler that records each *subscriber.ClosedError's message into r, and fails on any
// other error.
func recordClosed(t *testing.T, r *recorder[int]) func(context.Context, error) {
	t.Helper()

	return func(_ context.Context, err error) {
		var closedErr *subscriber.ClosedError[int]
		if !errors.As(err, &closedErr) || !errors.Is(err, subscriber.ErrClosed) {
			t.Errorf("error handler got %v, want a *subscriber.ClosedError", err)

			return
		}
		r.record(closedErr.Message)
	}
}
