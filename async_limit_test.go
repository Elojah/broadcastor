package broadcastor_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

// Past the limit, an async Broadcast waits for a free slot until its ctx is done, then reports a *TimeoutError. Once
// the sends holding the slots are done, async Broadcasts no longer wait.
func TestBroadcastorWithAsyncLimit(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int](broadcastor.WithAsyncLimit[int](2))
		stuck := &recorder[int]{hold: make(chan struct{})}
		timeouts := &recorder[int]{}
		id := subscribe(t, b, stuck.handle, subscriber.WithErrorHandler[int](recordTimeouts(t, timeouts)))
		b.Broadcast(t.Context(), 0)
		synctest.Wait() // stuck is now processing 0 and not reading

		// Both slots now hold a send waiting for stuck.
		for _, n := range []int{1, 2} {
			if got := b.Broadcast(t.Context(), n, message.WithAsync[int]()); got != 1 {
				t.Errorf("async Broadcast(%d) = %d with a free slot, want 1", n, got)
			}
		}

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		if got := b.Broadcast(ctx, 3, message.WithAsync[int]()); got != 0 {
			t.Errorf("async Broadcast = %d with no free slot, want 0", got)
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("async Broadcast returned after %v with no free slot, want %v", elapsed, time.Second)
		}
		if got, want := timeouts.messages(), []int{3}; !slices.Equal(got, want) {
			t.Errorf("timed out %v, want %v", got, want)
		}
		if got := b.Stats()[0].TimedOut; got != 1 {
			t.Errorf("Stats.TimedOut = %d, want 1", got)
		}

		stuck.release()
		synctest.Wait()
		start = time.Now()
		b.Broadcast(t.Context(), 4, message.WithAsync[int]())
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("async Broadcast returned after %v once the slots were free, want no wait", elapsed)
		}
		synctest.Wait()
		// 1 and 2 were sent from two goroutines at once.
		got := stuck.messages()
		slices.Sort(got)
		if want := []int{0, 1, 2, 4}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber received %v, want %v", got, want)
		}

		unsubscribe(t, b, id)
	})
}

// An async Broadcast waiting for a slot gets it once a send holding one is done.
func TestBroadcastorWithAsyncLimit_Wait(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int](broadcastor.WithAsyncLimit[int](1))
		stuck, id := hold(t, b)
		b.Broadcast(t.Context(), 0)
		synctest.Wait() // stuck is now processing 0 and not reading
		b.Broadcast(t.Context(), 1, message.WithAsync[int]())

		go func() {
			time.Sleep(time.Second)
			stuck.release()
		}()
		start := time.Now()
		if got := b.Broadcast(t.Context(), 2, message.WithAsync[int]()); got != 1 {
			t.Errorf("async Broadcast = %d once a slot was free, want 1", got)
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("async Broadcast returned after %v, want %v, when stuck was released", elapsed, time.Second)
		}
		synctest.Wait()
		if got, want := stuck.messages(), []int{0, 1, 2}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber received %v, want %v", got, want)
		}

		unsubscribe(t, b, id)
	})
}

// The message's timeout counts from when Broadcast gets to the subscriber, and covers the wait for a slot and the send
// together.
func TestBroadcastorWithAsyncLimit_Timeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int](broadcastor.WithAsyncLimit[int](1))
		// Each value on step lets handle return once.
		step := make(chan struct{})
		stepped := &recorder[int]{}
		var start time.Time // set before the Broadcast that reports
		timedOutAfter := make(chan time.Duration, 1)
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, n int) error {
			stepped.record(n)
			<-step

			return nil
		}, subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
			var timeout *subscriber.TimeoutError[int]
			if !errors.As(err, &timeout) || timeout.Message != 2 || !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("error handler got %v, want a *TimeoutError for message 2 wrapping %v", err, context.DeadlineExceeded)
			}
			timedOutAfter <- time.Since(start)
		}))
		b.Broadcast(t.Context(), 0)
		synctest.Wait()                                       // handle is now processing 0 and not reading
		b.Broadcast(t.Context(), 1, message.WithAsync[int]()) // holds the slot

		go func() {
			time.Sleep(600 * time.Millisecond)
			step <- struct{}{} // handle returns for 0 and takes 1, freeing the slot
		}()
		start = time.Now()
		if got := b.Broadcast(t.Context(), 2, message.WithAsync[int](), message.WithTimeout[int](time.Second)); got != 1 {
			t.Errorf("async Broadcast = %d once a slot was free, want 1", got)
		}
		if elapsed := <-timedOutAfter; elapsed != time.Second {
			t.Errorf("the send timed out %v after Broadcast got to the subscriber, want %v", elapsed, time.Second)
		}

		close(step)
		synctest.Wait()
		if got, want := stepped.messages(), []int{0, 1}; !slices.Equal(got, want) {
			t.Errorf("subscriber received %v, want %v", got, want)
		}
		unsubscribe(t, b, id)
	})
}

// The limit is shared: a stuck subscriber holding every slot makes async Broadcasts to the others wait and time out
// too.
func TestBroadcastorWithAsyncLimit_Shared(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int](broadcastor.WithAsyncLimit[int](1))
		stuck, stuckID := hold(t, b)
		b.Broadcast(t.Context(), 0)
		synctest.Wait() // stuck is now processing 0 and not reading
		b.Broadcast(t.Context(), 1, message.WithAsync[int]())
		// Only now, so that the send to stuck is the one holding the slot.
		reader := &recorder[int]{}
		timeouts := &recorder[int]{}
		readerID := subscribe(t, b, reader.handle, subscriber.WithErrorHandler[int](recordTimeouts(t, timeouts)))

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if got := b.Broadcast(ctx, 2, message.WithAsync[int]()); got != 0 {
			t.Errorf("async Broadcast = %d with no free slot, want 0", got)
		}
		if got, want := timeouts.messages(), []int{2}; !slices.Equal(got, want) {
			t.Errorf("reader timed out %v, want %v", got, want)
		}
		if got := reader.messages(); len(got) != 0 {
			t.Errorf("reader received %v, want nothing", got)
		}

		stuck.release()
		unsubscribeAll(t, b, stuckID, readerID)
	})
}

// An async Broadcast from handle, while every slot holds a send to its own subscriber, gives up once its ctx is done
// instead of waiting on itself for good.
func TestBroadcastorWithAsyncLimit_FromHandle(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int](broadcastor.WithAsyncLimit[int](1))
		handled := &recorder[int]{}
		timeouts := &recorder[int]{}
		done := make(chan struct{})
		id := subscribe(t, b, func(ctx context.Context, _ uuid.UUID, n int) error {
			handled.record(n)
			if n != 0 {
				return nil
			}
			defer close(done)
			b.Broadcast(ctx, 1, message.WithAsync[int]()) // holds the slot, waiting for this handle
			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if got := b.Broadcast(ctx, 2, message.WithAsync[int]()); got != 0 {
				t.Errorf("async Broadcast from handle = %d with no free slot, want 0", got)
			}

			return nil
		}, subscriber.WithErrorHandler[int](recordTimeouts(t, timeouts)))
		b.Broadcast(t.Context(), 0)
		waitClosed(t, done, "handle")
		synctest.Wait()
		if got, want := handled.messages(), []int{0, 1}; !slices.Equal(got, want) {
			t.Errorf("subscriber received %v, want %v", got, want)
		}
		if got, want := timeouts.messages(), []int{2}; !slices.Equal(got, want) {
			t.Errorf("timed out %v, want %v", got, want)
		}

		unsubscribe(t, b, id)
	})
}

// 0 or less means no limit.
func TestBroadcastorWithAsyncLimit_None(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, -1} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := broadcastor.NewBroadcastor[int](broadcastor.WithAsyncLimit[int](n))
				stuck, id := hold(t, b)
				b.Broadcast(t.Context(), 0)
				synctest.Wait() // stuck is now processing 0 and not reading

				start := time.Now()
				for i := 1; i <= 10; i++ {
					if got := b.Broadcast(t.Context(), i, message.WithAsync[int]()); got != 1 {
						t.Errorf("async Broadcast = %d, want 1", got)
					}
				}
				if elapsed := time.Since(start); elapsed != 0 {
					t.Errorf("async Broadcasts took %v, want no wait", elapsed)
				}

				stuck.release()
				synctest.Wait()
				if got := len(stuck.messages()); got != 11 {
					t.Errorf("stuck subscriber received %d messages, want 11", got)
				}
				unsubscribe(t, b, id)
			})
		})
	}
}
