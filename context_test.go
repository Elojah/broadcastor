package broadcastor_test

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/subscriber"
)

// Once its ctx is done, a subscriber is unsubscribed: Broadcast no longer reaches it, and its goroutine ends.
func TestSubscribe_ContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		id, err := b.Subscribe(ctx, handled.handle)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		b.Broadcast(t.Context(), 1)
		cancel()
		synctest.Wait()

		if n := b.Broadcast(t.Context(), 2); n != 0 {
			t.Errorf("Broadcast once ctx is done handed the message to %d subscribers, want 0", n)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once ctx is done = %v, want a *SubscriberNotFoundError", err)
		}
		synctest.Wait()
		if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

// A subscriber whose ctx is done while handle is running is unsubscribed right away, without waiting for handle, and
// with its default unsubscribe options: here, it discards what is left in its buffer.
func TestSubscribe_ContextDoneWhileHandling(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{hold: make(chan struct{})}
		closed := &recorder[int]{}
		id, err := b.Subscribe(ctx, handled.handle, subscriber.WithBuffer[int](1),
			subscriber.WithErrorHandler[int](recordClosed(t, closed)),
			subscriber.WithDefaultUnsubscribeOptions[int](subscriber.WithUnsubscribeDiscard()))
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		// handle holds on to 1, and 2 fills the buffer.
		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		cancel()
		synctest.Wait()

		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once ctx is done = %v, want a *SubscriberNotFoundError", err)
		}
		// Still subscribed, the subscriber would hold this Broadcast up until handle returns.
		if n := b.Broadcast(t.Context(), 3); n != 0 {
			t.Errorf("Broadcast once ctx is done handed the message to %d subscribers, want 0", n)
		}
		handled.release()
		synctest.Wait()

		if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		if got, want := closed.messages(), []int{2}; !slices.Equal(got, want) {
			t.Errorf("error handler got *subscriber.ClosedError for messages %v, want %v", got, want)
		}
	})
}

// A subscriber whose ctx is already done when it subscribes is unsubscribed right after.
func TestSubscribe_ContextDoneFirst(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		if _, err := b.Subscribe(ctx, handled.handle); err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		synctest.Wait()

		if n := b.Broadcast(t.Context(), 1); n != 0 {
			t.Errorf("Broadcast handed the message to %d subscribers, want 0", n)
		}
		synctest.Wait()
		if got := handled.messages(); len(got) != 0 {
			t.Errorf("handle got %v, want nothing", got)
		}
	})
}

// A SubscribeSeq subscriber whose ctx is done before its loop starts is unsubscribed without waiting for the loop, so
// Broadcast no longer waits for it. The loop then yields nothing, and reports the message the subscriber took.
func TestSubscribeSeq_ContextDoneBeforeLoop(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		closed := &recorder[int]{}
		_, seq, err := b.SubscribeSeq(ctx, subscriber.WithBuffer[int](1),
			subscriber.WithErrorHandler[int](recordClosed(t, closed)))
		if err != nil {
			t.Fatalf("SubscribeSeq: %v", err)
		}

		b.Broadcast(t.Context(), 1) // fills the buffer
		cancel()
		synctest.Wait()

		// Still subscribed, the subscriber would hold this Broadcast up until the loop starts.
		if n := b.Broadcast(t.Context(), 2); n != 0 {
			t.Errorf("Broadcast once ctx is done handed the message to %d subscribers, want 0", n)
		}
		for msg := range seq {
			t.Errorf("loop got %d, want nothing once ctx is done", msg)
		}
		synctest.Wait()
		if got, want := closed.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("error handler got *subscriber.ClosedError for messages %v, want %v", got, want)
		}
	})
}

// A subscriber unsubscribed another way, while its ctx is never done, leaves nothing waiting on that ctx. A goroutine
// left waiting would never end, and fail the synctest test.
func TestSubscribe_ContextNoLeak(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		close func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID)
	}{
		{"Unsubscribe", func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID) {
			t.Helper()
			unsubscribe(t, b, id)
		}},
		{"Close", func(t *testing.T, b *broadcastor.Broadcastor[int], _ uuid.UUID) {
			t.Helper()
			if err := b.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				ctx := neverDone{Context: context.Background(), done: make(chan struct{})}
				b := broadcastor.NewBroadcastor[int]()
				handled := &recorder[int]{}
				id, err := b.Subscribe(ctx, handled.handle)
				if err != nil {
					t.Fatalf("Subscribe: %v", err)
				}

				b.Broadcast(t.Context(), 1)
				tt.close(t, b, id)
				synctest.Wait()

				if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
					t.Errorf("handle got %v, want %v", got, want)
				}
			})
		})
	}
}

// Its ctx being done and Unsubscribe racing on one subscriber unsubscribe it once between them.
func TestSubscribe_ContextDoneRacesUnsubscribe(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		id, err := b.Subscribe(ctx, handled.handle)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		go cancel()
		if err := b.Unsubscribe(t.Context(), id); err != nil && !isNotFound(err) {
			t.Errorf("Unsubscribe = %v, want nil or a *SubscriberNotFoundError", err)
		}
		synctest.Wait()

		if n := b.Broadcast(t.Context(), 1); n != 0 {
			t.Errorf("Broadcast handed the message to %d subscribers, want 0", n)
		}
	})
}

// A subscriber with subscriber.WithDetachedContext stays subscribed once its ctx is done, and handle gets a ctx with
// the same values that is never done.
func TestSubscriberWithDetachedContext(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, "subscribe"))
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		id, err := b.Subscribe(ctx, func(ctx context.Context, id uuid.UUID, msg int) error {
			if err := ctx.Err(); err != nil {
				t.Errorf("handle got a done ctx: %v", err)
			}
			if got, want := ctx.Value(key{}), "subscribe"; got != want {
				t.Errorf("handle got ctx value %v, want %v", got, want)
			}

			return handled.handle(ctx, id, msg)
		}, subscriber.WithDetachedContext[int]())
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		cancel()
		synctest.Wait()

		if n := b.Broadcast(t.Context(), 1); n != 1 {
			t.Errorf("Broadcast once ctx is done handed the message to %d subscribers, want 1", n)
		}
		unsubscribe(t, b, id)
		synctest.Wait()
		if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

// A SubscribeSeq loop with subscriber.WithDetachedContext goes on once its ctx is done, and ends once its subscriber is
// unsubscribed.
func TestSubscriberWithDetachedContext_SubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		id, seq, err := b.SubscribeSeq(ctx, subscriber.WithDetachedContext[int]())
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
		cancel()
		synctest.Wait()

		if n := b.Broadcast(t.Context(), 1); n != 1 {
			t.Errorf("Broadcast once ctx is done handed the message to %d subscribers, want 1", n)
		}
		unsubscribe(t, b, id)
		waitClosed(t, done, "the loop to end once its subscriber is unsubscribed")
		if got, want := got.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("loop got %v, want %v", got, want)
		}
	})
}

// neverDone is a ctx that is never done, of a type the context package does not know, so a ctx derived from it needs a
// goroutine to wait for it, which only ends once that derived ctx is cancelled.
type neverDone struct {
	context.Context //nolint:containedctx // it is the ctx itself, not a struct carrying one

	done chan struct{}
}

func (c neverDone) Done() <-chan struct{} {
	return c.done
}
