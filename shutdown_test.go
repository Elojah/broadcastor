package broadcastor_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

// Shutdown returns once every subscriber has handled what it took: the message in handle, its buffer, and what a
// SubscribeSeq loop took. Every later call returns ErrClosed.
func TestShutdown(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		release := make(chan struct{})
		handled := &recorder[int]{hold: release}
		subscribe(t, b, handled.handle, subscriber.WithBuffer[int](2))
		_, seq := subscribeSeq(t, b, subscriber.WithBuffer[int](2))
		yielded := &recorder[int]{}
		go func() {
			for msg := range seq {
				yielded.record(msg)
				<-release
			}
		}()

		// handle and the loop hold on to 1, with 2 and 3 in their buffers.
		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		errs := shutdown(t.Context(), b)
		checkWaiting(t, errs, "handle and the loop held message 1")
		close(release)
		if err := waitShutdown(t, errs); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}

		// Without synctest.Wait: Shutdown returning is enough.
		for name, r := range map[string]*recorder[int]{"Subscribe": handled, "SubscribeSeq": yielded} {
			if got, want := r.messages(), []int{1, 2, 3}; !slices.Equal(got, want) {
				t.Errorf("%s subscriber got %v once Shutdown returned, want %v", name, got, want)
			}
		}
		if err := b.Shutdown(t.Context()); !errors.Is(err, broadcastor.ErrClosed) {
			t.Errorf("second Shutdown = %v, want ErrClosed", err)
		}
		if err := b.Close(); !errors.Is(err, broadcastor.ErrClosed) {
			t.Errorf("Close after Shutdown = %v, want ErrClosed", err)
		}
		if _, err := b.Subscribe(t.Context(), handled.handle); !errors.Is(err, broadcastor.ErrClosed) {
			t.Errorf("Subscribe after Shutdown = %v, want ErrClosed", err)
		}
	})
}

// With WithUnsubscribeDiscard, Shutdown returns once the subscriber has reported what was left in its buffer, so its
// dead letters hold it.
func TestShutdown_Discard(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{hold: make(chan struct{})}
		stored := &recordStore{}
		subscribe(t, b, handled.handle, subscriber.WithBuffer[int](2), subscriber.WithDeadLetters[int](stored),
			subscriber.WithUnsubscribeOptions[int](subscriber.WithUnsubscribeDiscard()))

		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		// Else Consume may discard 1 too, if it took it but has not called handle yet.
		synctest.Wait() // handle holds on to 1, with 2 and 3 in the buffer
		errs := shutdown(t.Context(), b)
		checkWaiting(t, errs, "handle held message 1")
		handled.release()
		if err := waitShutdown(t, errs); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}

		if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		records := stored.messages()
		got := make([]int, 0, len(records))
		for _, r := range records {
			if !errors.Is(r.Err, subscriber.ErrClosed) {
				t.Errorf("store got message %d with error %v, want a *subscriber.ClosedError", r.Message, r.Err)
			}
			got = append(got, r.Message)
		}
		if want := []int{2, 3}; !slices.Equal(got, want) {
			t.Errorf("store got %v once Shutdown returned, want %v", got, want)
		}
	})
}

// Shutdown frees the Broadcasts waiting on a subscriber, sync and async, and returns once they have reported their
// messages and handle has returned.
func TestShutdown_FreesBroadcast(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{hold: make(chan struct{})}
		closed := &recorder[int]{}
		subscribe(t, b, handled.handle, subscriber.WithErrorHandler[int](recordClosed(t, closed)))

		b.Broadcast(t.Context(), 1) // handle holds on to it
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Broadcast(t.Context(), 2)
		}()
		b.Broadcast(t.Context(), 3, message.WithAsync[int]())
		synctest.Wait() // both sends wait on the subscriber

		errs := shutdown(t.Context(), b)
		waitClosed(t, done, "Broadcast of 2 once shut down")
		checkWaiting(t, errs, "handle held message 1")
		handled.release()
		if err := waitShutdown(t, errs); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}

		if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		// Both sends gave up at once, in any order.
		if got, want := slices.Sorted(slices.Values(closed.messages())), []int{2, 3}; !slices.Equal(got, want) {
			t.Errorf("error handler got *subscriber.ClosedError for messages %v once Shutdown returned, want %v", got, want)
		}
	})
}

// Shutdown gives up once its ctx is done, here while handle hangs. A later call waits again.
func TestShutdown_ContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		handled, _ := hold(t, b)
		b.Broadcast(t.Context(), 1)

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := b.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown while handle hangs = %v, want %v", err, context.DeadlineExceeded)
		}

		errs := shutdown(t.Context(), b)
		checkWaiting(t, errs, "handle held message 1")
		handled.release()
		if err := waitShutdown(t, errs); !errors.Is(err, broadcastor.ErrClosed) {
			t.Errorf("second Shutdown = %v, want ErrClosed", err)
		}
	})
}

// handle shuts the Broadcastor down while a Broadcast waits on it: Shutdown waits on that handle until its ctx is done,
// whereas the Broadcast gives up.
func TestShutdown_FromHandle(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		self := &recorder[int]{}
		proceed := make(chan struct{})
		errs := make(chan error, 1)
		subscribe(t, b, func(ctx context.Context, _ uuid.UUID, msg int) error {
			self.record(msg)
			if msg == 1 {
				<-proceed
				ctx, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				errs <- b.Shutdown(ctx)
			}

			return nil
		})

		b.Broadcast(t.Context(), 1) // self is now processing 1 and not reading
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Broadcast(t.Context(), 2)
		}()
		synctest.Wait() // the Broadcast of 2 is waiting on self
		close(proceed)
		waitClosed(t, done, "Broadcast during which handle shut the Broadcastor down")
		if err := waitShutdown(t, errs); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown from handle = %v, want %v", err, context.DeadlineExceeded)
		}
		synctest.Wait()

		if got, want := self.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		if _, err := b.Subscribe(t.Context(), self.handle); !errors.Is(err, broadcastor.ErrClosed) {
			t.Errorf("Subscribe after Shutdown = %v, want ErrClosed", err)
		}
	})
}

// A SubscribeSeq loop that never started holds Shutdown until its ctx is done, since the subscriber still has a message
// to yield. Once ranged, the loop yields it and ends, and Shutdown returns.
func TestShutdown_SubscribeSeqNotRanged(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		_, seq := subscribeSeq(t, b, subscriber.WithBuffer[int](1))
		b.Broadcast(t.Context(), 1) // fills the buffer

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := b.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown before the loop started = %v, want %v", err, context.DeadlineExceeded)
		}

		yielded := &recorder[int]{}
		for msg := range seq {
			yielded.record(msg)
		}
		if err := waitShutdown(t, shutdown(t.Context(), b)); !errors.Is(err, broadcastor.ErrClosed) {
			t.Errorf("Shutdown once the loop ended = %v, want ErrClosed", err)
		}
		if got, want := yielded.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("loop got %v, want %v", got, want)
		}
	})
}

// shutdown calls b.Shutdown in a goroutine, and returns a channel that yields its error.
func shutdown[T any](ctx context.Context, b *broadcastor.Broadcastor[T]) <-chan error {
	errs := make(chan error, 1)
	go func() { errs <- b.Shutdown(ctx) }()

	return errs
}

// waitShutdown returns the error errs yields, or fails after deadlockTimeout.
func waitShutdown(t *testing.T, errs <-chan error) error {
	t.Helper()
	select {
	case err := <-errs:
		return err
	case <-time.After(deadlockTimeout):
		t.Fatalf("still waiting for Shutdown after %v: deadlock", deadlockTimeout)

		return nil
	}
}

// checkWaiting fails the test if errs yielded already, once every goroutine of the bubble is blocked.
func checkWaiting(t *testing.T, errs <-chan error, while string) {
	t.Helper()
	synctest.Wait()
	select {
	case err := <-errs:
		t.Fatalf("Shutdown returned %v while %s", err, while)
	default:
	}
}
