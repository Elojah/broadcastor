package multicast_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/elojah/multicast/subscriber"
)

// SubscribeSeq yields every message the subscriber takes, in order, and breaking out of the loop unsubscribes it.
func TestSubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newGroup[int](t)
		_, seq := subscribeSeq(t, b)

		go func() {
			for msg := 1; msg <= 3; msg++ {
				b.Broadcast(t.Context(), msg)
			}
		}()
		var got []int
		for msg := range seq {
			got = append(got, msg)
			if msg == 3 {
				break
			}
		}
		if want := []int{1, 2, 3}; !slices.Equal(got, want) {
			t.Errorf("loop got %v, want %v", got, want)
		}

		if n := b.Broadcast(t.Context(), 4); n != 0 {
			t.Errorf("Broadcast after the loop ended handed the message to %d subscribers, want 0", n)
		}
		synctest.Wait()
	})
}

// After a break, what was taken but not yielded is reported as *subscriber.ClosedError with the SubscribeSeq ctx, and a
// waiting Broadcast returns.
func TestSubscribeSeq_BreakReportsUnyielded(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		ctx := context.WithValue(subscribeCtx(t), key{}, "seq")
		b := newGroup[int](t)
		closed := &recorder[int]{}
		id, seq, err := b.SubscribeSeq(ctx, subscriber.WithBuffer[int](2),
			subscriber.WithErrorHandler[int](func(ctx context.Context, err error) {
				var closedErr *subscriber.ClosedError[int]
				if !errors.As(err, &closedErr) || !errors.Is(err, subscriber.ErrClosed) {
					t.Errorf("error handler got %v, want a *subscriber.ClosedError", err)

					return
				}
				if v := ctx.Value(key{}); v != "seq" {
					t.Errorf("error handler got a ctx with value %v, want the one passed to SubscribeSeq", v)
				}
				closed.record(closedErr.Message)
			}))
		if err != nil {
			t.Fatalf("SubscribeSeq: %v", err)
		}

		// Nothing reads before the loop starts: 1 and 2 fill the buffer, and the Broadcast of 3 waits.
		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Broadcast(t.Context(), 3)
		}()
		synctest.Wait()

		for msg := range seq {
			if msg != 1 {
				t.Errorf("loop got %d first, want 1", msg)
			}

			break
		}
		waitClosed(t, done, "Broadcast to a subscriber whose loop has ended")
		synctest.Wait()

		// 3 is reported by the Broadcast, and 2 by the loop's discard, in either order.
		if got, want := slices.Sorted(slices.Values(closed.messages())), []int{2, 3}; !slices.Equal(got, want) {
			t.Errorf("error handler got *subscriber.ClosedError for messages %v, want %v", got, want)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe after the loop ended = %v, want *SubscriberNotFoundError", err)
		}
	})
}

// Once ctx is done, a loop waiting for its next message ends and unsubscribes.
func TestSubscribeSeq_ContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := newGroup[int](t)
		_, seq, err := b.SubscribeSeq(ctx)
		if err != nil {
			t.Fatalf("SubscribeSeq: %v", err)
		}

		got := &recorder[int]{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for msg := range seq {
				got.record(msg)
			}
		}()
		b.Broadcast(t.Context(), 1)
		synctest.Wait() // the loop is waiting for its next message

		cancel()
		waitClosed(t, done, "the loop to end once ctx is done")
		if n := b.Broadcast(t.Context(), 2); n != 0 {
			t.Errorf("Broadcast after the loop ended handed the message to %d subscribers, want 0", n)
		}
		synctest.Wait()
		if got, want := got.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("loop got %v, want %v", got, want)
		}
	})
}

// A loop whose ctx is already done yields nothing, even with messages waiting, and reports them.
func TestSubscribeSeq_ContextDoneFirst(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := newGroup[int](t)
		errs := &recorder[error]{}
		_, seq, err := b.SubscribeSeq(ctx, subscriber.WithBuffer[int](1),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
				errs.record(err)
			}))
		if err != nil {
			t.Fatalf("SubscribeSeq: %v", err)
		}

		b.Broadcast(t.Context(), 1)
		cancel()
		for msg := range seq {
			t.Errorf("loop got %d, want nothing once ctx is done", msg)
		}
		synctest.Wait()
		if got := errs.messages(); len(got) != 1 || !errors.Is(got[0], subscriber.ErrClosed) {
			t.Errorf("error handler got %v, want a single *subscriber.ClosedError", got)
		}
	})
}

// fail's error is handle's: the error handler gets it as is, and Stats count Failed. Not calling fail, or nil last,
// counts Handled.
func TestSubscribeSeq_Fail(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newGroup[int](t)
		failures := &recorder[int]{}
		id, seq := subscribeSeq(t, b, subscriber.WithErrorHandler[int](recordFailures(t, failures)))

		done := make(chan struct{})
		go func() {
			defer close(done)
			for msg, fail := range seq {
				switch msg {
				case 1:
					fail(handleError(msg))
				case 2:
					fail(handleError(msg))
					fail(nil)
				}
			}
		}()
		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		synctest.Wait()

		if got, want := failures.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("error handler got failures for messages %v, want %v", got, want)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 3, Handled: 2, Failed: 1})

		unsubscribe(t, b, id)
		waitClosed(t, done, "the loop to end once unsubscribed")
		synctest.Wait()
	})
}

// The iterator can be ranged over once: a range after it, or alongside it, yields nothing.
func TestSubscribeSeq_RangeOnce(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newGroup[int](t)
		_, seq := subscribeSeq(t, b)

		done := make(chan struct{})
		go func() {
			defer close(done)
			for range seq {
				break
			}
		}()
		synctest.Wait() // the first loop is waiting for its next message

		for msg := range seq {
			t.Errorf("a second loop alongside the first got %d, want nothing", msg)
		}
		if n := b.Broadcast(t.Context(), 1); n != 1 {
			t.Errorf("Broadcast handed the message to %d subscribers, want 1", n)
		}
		waitClosed(t, done, "the first loop to break")

		for msg := range seq {
			t.Errorf("a loop after the first got %d, want nothing", msg)
		}
		synctest.Wait()
	})
}

// A panic in the loop body reaches the caller, unsubscribes, and reports the message as a *subscriber.ClosedError.
func TestSubscribeSeq_Panic(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newGroup[int](t)
		closed := &recorder[int]{}
		_, seq := subscribeSeq(t, b, subscriber.WithErrorHandler[int](recordClosed(t, closed)))

		go b.Broadcast(t.Context(), 1)
		p := recovered(func() {
			for msg := range seq {
				panic(handleError(msg))
			}
		})
		if p != handleError(1) {
			t.Errorf("loop panicked with %v, want %v", p, handleError(1))
		}
		if n := b.Broadcast(t.Context(), 2); n != 0 {
			t.Errorf("Broadcast after the loop panicked handed the message to %d subscribers, want 0", n)
		}
		synctest.Wait()
		if got, want := closed.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("error handler got *subscriber.ClosedError for messages %v, want %v", got, want)
		}
	})
}
