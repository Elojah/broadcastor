package broadcastor_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

// The replay comes before any Broadcast, in order, through the middlewares, error handlers and dead letters, with the
// default message options, each value counted like a message and acked once handled or reported.
func TestSubscriberWithReplay(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		events := &recorder[string]{}
		middleware := func(next subscriber.Handler[int]) subscriber.Handler[int] {
			return func(ctx context.Context, id uuid.UUID, msg int) error {
				events.record(fmt.Sprintf("middleware %d", msg))

				return next(ctx, id, msg)
			}
		}
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			if msg%2 == 0 {
				return handleError(msg)
			}

			return nil
		},
			subscriber.WithReplay(replayOf(events, 1, 2, 3)),
			subscriber.WithMiddleware(middleware),
			subscriber.WithDeadLetters[int](storeFunc(func(_ context.Context, r subscriber.Record[int]) error {
				events.record(fmt.Sprintf("stored %d", r.Message))

				return nil
			})),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) { events.record("reported " + err.Error()) }),
			subscriber.WithDefaultMessageOptions(message.WithErrorHandler[int](func(_ context.Context, err error) {
				events.record("message reported " + err.Error())
			})),
		)

		// It waits until the subscriber has replayed 1 to 3, and counts none of them.
		if n := b.Broadcast(t.Context(), 4); n != 1 {
			t.Errorf("Broadcast handed 4 to %d subscribers, want 1", n)
		}
		synctest.Wait()
		want := []string{
			"middleware 1", "acked 1",
			"middleware 2", "stored 2", "reported handling 2 failed", "message reported handling 2 failed", "acked 2",
			"middleware 3", "acked 3",
			"middleware 4", "stored 4", "reported handling 4 failed", "message reported handling 4 failed",
		}
		if got := events.messages(); !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 4, Handled: 2, Failed: 2})

		unsubscribe(t, b, id)
		synctest.Wait()
	})
}

// Once the subscriber discards, yield returns false without handling the value, and the replay keeps the rest. Without
// discard, it keeps replaying once unsubscribed, and Shutdown waits for it.
func TestSubscriberWithReplay_Unsubscribe(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		remove func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID) <-chan error
		want   []string
	}{
		{"Discard", func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID) <-chan error {
			t.Helper()
			if err := b.Unsubscribe(t.Context(), id, subscriber.WithUnsubscribeDiscard()); err != nil {
				t.Fatalf("Unsubscribe: %v", err)
			}

			return nil
		}, []string{"handled 1", "acked 1", "stopped at 2"}},
		{"Shutdown", func(t *testing.T, b *broadcastor.Broadcastor[int], _ uuid.UUID) <-chan error {
			t.Helper()
			errs := shutdown(t.Context(), b)
			checkWaiting(t, errs, "the subscriber replays")

			return errs
		}, []string{"handled 1", "acked 1", "handled 2", "acked 2", "handled 3", "acked 3"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := broadcastor.NewBroadcastor[int]()
				events := &recorder[string]{}
				hold := make(chan struct{})
				id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
					events.record(fmt.Sprintf("handled %d", msg))
					<-hold

					return nil
				},
					subscriber.WithReplay(replayOf(events, 1, 2, 3)),
					subscriber.WithErrorHandler[int](func(_ context.Context, err error) { t.Errorf("error handler got %v", err) }),
				)

				synctest.Wait() // handle holds on to 1
				errs := tt.remove(t, b, id)
				close(hold)
				if errs != nil {
					if err := waitShutdown(t, errs); err != nil {
						t.Fatalf("Shutdown: %v", err)
					}
				}
				synctest.Wait()
				if got := events.messages(); !slices.Equal(got, tt.want) {
					t.Errorf("got %q, want %q", got, tt.want)
				}
			})
		})
	}
}

// A Broadcast during the replay waits as for a busy handle, and its timeout reaches WithEvict: eviction ends the
// replay.
func TestSubscriberWithReplay_BroadcastWaits(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		events := &recorder[string]{}
		hold := make(chan struct{})
		subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			events.record(fmt.Sprintf("handled %d", msg))
			<-hold

			return nil
		},
			subscriber.WithReplay(replayOf(events, 1, 2)),
			subscriber.WithTimeout[int](time.Second),
			subscriber.WithEvict[int](isTimeout, nil),
			subscriber.WithErrorHandler[int](recordLosses(t, events)),
		)

		synctest.Wait() // handle holds on to 1
		start := time.Now()
		if n := b.Broadcast(t.Context(), 3); n != 0 {
			t.Errorf("Broadcast handed 3 to %d subscribers, want 0", n)
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("Broadcast returned after %v, want the timeout, %v", elapsed, time.Second)
		}
		close(hold)
		synctest.Wait()
		if got, want := events.messages(), []string{"handled 1", "3 evicted, timed out", "acked 1", "stopped at 2"}; !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// A replayed value's error reaches WithEvict like handle's: eviction ends the replay, and the rest stays in the source.
func TestSubscriberWithReplay_Evicted(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		events := &recorder[string]{}
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			events.record(fmt.Sprintf("handled %d", msg))

			return handleError(msg)
		},
			subscriber.WithReplay(replayOf(events, 1, 2, 3)),
			subscriber.WithEvict[int](func(err error) bool { return errors.Is(err, handleError(2)) }, nil),
			subscriber.WithErrorHandler[int](recordLosses(t, events)),
		)
		synctest.Wait()

		want := []string{"handled 1", "1 failed", "acked 1", "handled 2", "2 evicted, failed", "acked 2", "stopped at 3"}
		if got := events.messages(); !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once evicted = %v, want a *SubscriberNotFoundError", err)
		}
	})
}

// A SubscribeSeq loop gets the replay first, once ranged.
func TestSubscriberWithReplay_SubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		events := &recorder[string]{}
		_, seq := subscribeSeq(t, b, subscriber.WithReplay(replayOf(events, 1, 2)))
		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			for msg := range seq {
				events.record(fmt.Sprintf("yielded %d", msg))
				if msg == 3 {
					break
				}
			}
		}()

		b.Broadcast(t.Context(), 3)
		waitClosed(t, loopDone, "SubscribeSeq loop")
		synctest.Wait()
		if got, want := events.messages(), []string{"yielded 1", "acked 1", "yielded 2", "acked 2", "yielded 3"}; !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// A loop that breaks during the replay ends it: the value it broke on was handled, and the rest stays in the source.
func TestSubscriberWithReplay_SubscribeSeqBreak(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		events := &recorder[string]{}
		_, seq := subscribeSeq(t, b, subscriber.WithReplay(replayOf(events, 1, 2, 3)),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) { t.Errorf("error handler got %v", err) }))
		for msg := range seq {
			events.record(fmt.Sprintf("yielded %d", msg))
			if msg == 2 {
				break
			}
		}
		synctest.Wait()
		want := []string{"yielded 1", "acked 1", "yielded 2", "acked 2", "stopped at 3"}
		if got := events.messages(); !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// replayOf returns a replay of values that records "acked <value>" into events once yield returned true for it, and
// "stopped at <value>" once it returned false.
func replayOf(events *recorder[string], values ...int) iter.Seq[int] {
	return func(yield func(int) bool) {
		for _, v := range values {
			if !yield(v) {
				events.record(fmt.Sprintf("stopped at %d", v))

				return
			}
			events.record(fmt.Sprintf("acked %d", v))
		}
	}
}
