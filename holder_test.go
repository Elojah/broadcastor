package broadcastor_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/holder"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

const holdFor = time.Second

// byTens returns a holder.Reduce keeping the latest message of each ten (11, 12 and 13 share one) for d.
func byTens(d time.Duration) *holder.Reduce[int] {
	return holder.NewReduce(d, func(held []int, next int) []int {
		for i, msg := range held {
			if msg/10 == next/10 {
				held[i] = next

				return held
			}
		}

		return append(held, next)
	})
}

// The subscriber holds each message it takes, and handles, through its middlewares, what the holder releases once
// due. Stats count the messages held and merged.
func TestSubscriberWithHolder(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		r := &recorder[int]{}
		wrapped := &recorder[int]{}
		id := subscribe(t, b, r.handle, subscriber.WithHolder(byTens(holdFor)),
			subscriber.WithMiddleware(func(next subscriber.Handler[int]) subscriber.Handler[int] {
				return func(ctx context.Context, id uuid.UUID, msg int) error {
					wrapped.record(msg)

					return next(ctx, id, msg)
				}
			}))

		for _, msg := range []int{11, 21, 12, 13} {
			b.Broadcast(t.Context(), msg)
		}
		synctest.Wait()
		if got := r.messages(); len(got) != 0 {
			t.Errorf("handle got %v within the window, want nothing yet", got)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 4, Held: 2, Merged: 2})

		time.Sleep(holdFor)
		synctest.Wait()
		want := []int{13, 21}
		if got := r.messages(); !slices.Equal(got, want) {
			t.Errorf("handle got %v once the window ended, want %v", got, want)
		}
		if got := wrapped.messages(); !slices.Equal(got, want) {
			t.Errorf("the middleware got %v, want %v", got, want)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 4, Handled: 2, Merged: 2})
	})
}

// A message taken once its window has ended starts another. TestStress checks that the subscriber releases what is
// due before holding a message, which a timer and a message coming together need.
func TestSubscriberWithHolder_WindowEnded(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		r := &recorder[int]{}
		subscribe(t, b, r.handle, subscriber.WithHolder(holder.NewLatest[int](holdFor)))
		b.Broadcast(t.Context(), 1)
		time.Sleep(holdFor)
		b.Broadcast(t.Context(), 2)
		time.Sleep(holdFor)
		synctest.Wait()
		if got, want := r.messages(), []int{1, 2}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

// A message released is handled with the ctx of the newest message merged into it, and handle's error reaches the
// subscriber's error handler, then the newest message's.
func TestSubscriberWithHolder_Errors(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		type key struct{}
		var handledWith any
		subscriberErrs, firstErrs, newestErrs := &recorder[int]{}, &recorder[int]{}, &recorder[int]{}
		subscribe(t, b, func(ctx context.Context, _ uuid.UUID, msg int) error {
			handledWith = ctx.Value(key{})

			return handleError(msg)
		}, subscriber.WithHolder(byTens(holdFor)), subscriber.WithErrorHandler[int](recordFailures(t, subscriberErrs)))

		b.Broadcast(t.Context(), 11, message.WithErrorHandler[int](recordFailures(t, firstErrs)))
		b.Broadcast(t.Context(), 12, message.WithErrorHandler[int](recordFailures(t, newestErrs)),
			message.WithContext[int](context.WithValue(t.Context(), key{}, "newest")))
		time.Sleep(holdFor)
		synctest.Wait()

		if handledWith != "newest" {
			t.Errorf("handle got a ctx with %v, want the newest message's", handledWith)
		}
		for _, tt := range []struct {
			name string
			errs *recorder[int]
			want []int
		}{
			{"subscriber's", subscriberErrs, []int{12}},
			{"first message's", firstErrs, nil},
			{"newest message's", newestErrs, []int{12}},
		} {
			if got := tt.errs.messages(); !slices.Equal(got, tt.want) {
				t.Errorf("the %s error handler got failures for %v, want %v", tt.name, got, tt.want)
			}
		}
	})
}

// Shutdown releases everything held at once, and waits for handle, then done.
func TestSubscriberWithHolder_Shutdown(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		r := &recorder[int]{}
		done := make(chan uuid.UUID, 1)
		id := subscribe(t, b, r.handle, subscriber.WithHolder(byTens(time.Hour)), subscriber.WithDone[int](t.Context(), done))
		for _, msg := range []int{11, 21, 12} {
			b.Broadcast(t.Context(), msg)
		}

		start := time.Now()
		if err := waitShutdown(t, shutdown(t.Context(), b)); err != nil {
			t.Fatalf("Shutdown = %v, want nil", err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Shutdown took %v, want no wait for the window", elapsed)
		}
		if got, want := r.messages(), []int{12, 21}; !slices.Equal(got, want) {
			t.Errorf("handle got %v once Shutdown returned, want %v", got, want)
		}
		if got := received(done); !slices.Equal(got, []uuid.UUID{id}) {
			t.Errorf("done got %v once Shutdown returned, want %v", got, id)
		}
	})
}

// Once unsubscribed with WithUnsubscribeDiscard, what the holder holds is reported as *subscriber.ClosedError.
func TestSubscriberWithHolder_Discard(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		r := &recorder[int]{}
		errs := &recorder[error]{}
		id := subscribe(t, b, r.handle, subscriber.WithHolder(byTens(time.Hour)),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) { errs.record(err) }))
		for _, msg := range []int{11, 21, 12} {
			b.Broadcast(t.Context(), msg)
		}

		if err := b.Unsubscribe(t.Context(), id, subscriber.WithUnsubscribeDiscard()); err != nil {
			t.Fatalf("Unsubscribe: %v", err)
		}
		synctest.Wait()
		if got := r.messages(); len(got) != 0 {
			t.Errorf("handle got %v, want nothing", got)
		}
		got := describeLosses(t, errs.messages())
		if want := []string{"12 closed", "21 closed"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %v, want %v", got, want)
		}
	})
}

// A message the holder has no room for is lost as a *subscriber.DroppedError, from the subscriber's goroutine, and
// counted in Dropped.
func TestSubscriberWithHolder_Full(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		r := &recorder[int]{}
		errs := &recorder[error]{}
		id := subscribe(t, b, r.handle, subscriber.WithHolder[int](&capped{n: 1, d: holdFor}),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) { errs.record(err) }))
		for _, msg := range []int{11, 21} {
			if n := b.Broadcast(t.Context(), msg); n != 1 {
				t.Errorf("Broadcast(%d) handed it to %d subscribers, want 1: the subscriber took it", msg, n)
			}
		}
		synctest.Wait()
		if got := errs.messages(); len(got) != 1 || describeLoss(t, got[0]) != "21 dropped" {
			t.Errorf("error handler got %v, want 21 dropped", got)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 2, Held: 1, Dropped: 1})

		time.Sleep(holdFor)
		synctest.Wait()
		if got, want := r.messages(), []int{11}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

// subscriber.WithEvict sees a holder's *subscriber.DroppedError, and evicts the subscriber, discarding what it holds.
func TestSubscriberWithHolder_Evict(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		r := &recorder[int]{}
		errs := &recorder[error]{}
		subscribe(t, b, r.handle, subscriber.WithHolder[int](&capped{n: 1, d: time.Hour}),
			subscriber.WithEvict[int](func(err error) bool { return errors.Is(err, subscriber.ErrDropped) }),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) { errs.record(err) }))
		b.Broadcast(t.Context(), 11)
		b.Broadcast(t.Context(), 21)
		synctest.Wait()

		if got := r.messages(); len(got) != 0 {
			t.Errorf("handle got %v, want nothing", got)
		}
		got := describeLosses(t, errs.messages())
		if want := []string{"21 evicted, dropped", "11 closed"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %v, want %v", got, want)
		}
		if stats := b.Stats(); len(stats) != 0 {
			t.Errorf("Stats once evicted are for %d subscribers, want none", len(stats))
		}
	})
}

// A SubscribeSeq loop body gets what the holder releases, and once unsubscribed, everything it still holds.
func TestSubscriberWithHolder_SubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		id, seq := subscribeSeq(t, b, subscriber.WithHolder(byTens(holdFor)))
		r := &recorder[int]{}
		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			for msg := range seq {
				r.record(msg)
			}
		}()

		for _, msg := range []int{11, 21, 12} {
			b.Broadcast(t.Context(), msg)
		}
		time.Sleep(holdFor)
		synctest.Wait()
		if got, want := r.messages(), []int{12, 21}; !slices.Equal(got, want) {
			t.Errorf("the loop got %v once the window ended, want %v", got, want)
		}

		b.Broadcast(t.Context(), 31)
		unsubscribe(t, b, id)
		waitClosed(t, loopDone, "SubscribeSeq loop ended by Unsubscribe")
		if got, want := r.messages(), []int{12, 21, 31}; !slices.Equal(got, want) {
			t.Errorf("the loop got %v once unsubscribed, want %v", got, want)
		}
	})
}

// Replayed values skip the holder: they are handled at once, before any message.
func TestSubscriberWithHolder_Replay(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		r := &recorder[int]{}
		subscribe(t, b, r.handle, subscriber.WithHolder(byTens(holdFor)), subscriber.WithReplay(slices.Values([]int{-1, -2})))
		b.Broadcast(t.Context(), 11)
		synctest.Wait()
		if got, want := r.messages(), []int{-1, -2}; !slices.Equal(got, want) {
			t.Errorf("handle got %v within the window, want only the replay %v", got, want)
		}
	})
}

// describeLosses describes each error with describeLoss.
func describeLosses(t *testing.T, errs []error) []string {
	t.Helper()
	got := make([]string, 0, len(errs))
	for _, err := range errs {
		got = append(got, describeLoss(t, err))
	}

	return got
}

// capped is a holder keeping each message for d, merging none, with room for n.
type capped struct {
	n    int
	d    time.Duration
	held []message.Message[int]
	due  []time.Time
}

func (c *capped) Hold(m message.Message[int], now time.Time) (int, bool) {
	if len(c.held) == c.n {
		return 0, false
	}
	c.held, c.due = append(c.held, m), append(c.due, now.Add(c.d))

	return 0, true
}

func (c *capped) Release(now time.Time, all bool) (message.Message[int], time.Duration, bool) {
	if len(c.held) == 0 {
		return message.Message[int]{}, 0, false
	}
	if wait := c.due[0].Sub(now); wait > 0 && !all {
		return message.Message[int]{}, wait, false
	}
	m := c.held[0]
	c.held, c.due = c.held[1:], c.due[1:]

	return m, 0, true
}
