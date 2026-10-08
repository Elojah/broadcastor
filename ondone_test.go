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
	"github.com/elojah/broadcastor/subscriber"
)

// onDone runs once, in order, after handle's last call, however the subscriber was removed: once it has handled its
// buffer by default, or reported it with discard.
func TestSubscriberWithOnDone(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		options []subscriber.Option[int]
		remove  func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID, cancel context.CancelFunc)
		want    []string
	}{
		{"Unsubscribe", nil, func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID, _ context.CancelFunc) {
			t.Helper()
			unsubscribe(t, b, id)
		}, []string{"handled 1", "handled 2", "done 1", "done 2"}},
		{"UnsubscribeDiscard", nil, func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID, _ context.CancelFunc) {
			t.Helper()
			if err := b.Unsubscribe(t.Context(), id, subscriber.WithUnsubscribeDiscard()); err != nil {
				t.Fatalf("Unsubscribe: %v", err)
			}
		}, []string{"handled 1", "2 closed", "done 1", "done 2"}},
		{"Close", nil, func(t *testing.T, b *broadcastor.Broadcastor[int], _ uuid.UUID, _ context.CancelFunc) {
			t.Helper()
			if err := b.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}, []string{"handled 1", "handled 2", "done 1", "done 2"}},
		{"ContextDone", nil, func(t *testing.T, _ *broadcastor.Broadcastor[int], _ uuid.UUID, cancel context.CancelFunc) {
			t.Helper()
			cancel()
		}, []string{"handled 1", "handled 2", "done 1", "done 2"}},
		// 3 finds the buffer full and evicts the subscriber, which then discards 2.
		{"Evicted", []subscriber.Option[int]{subscriber.WithEvict[int](isTimeout, nil), subscriber.WithTimeout[int](time.Second)},
			func(t *testing.T, b *broadcastor.Broadcastor[int], _ uuid.UUID, _ context.CancelFunc) {
				t.Helper()
				b.Broadcast(t.Context(), 3)
			}, []string{"handled 1", "3 evicted, timed out", "2 closed", "done 1", "done 2"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := broadcastor.NewBroadcastor[int]()
				events := &recorder[string]{}
				hold := make(chan struct{})
				ctx, cancel := context.WithCancel(subscribeCtx(t))
				defer cancel()
				options := append([]subscriber.Option[int]{
					subscriber.WithBuffer[int](1),
					subscriber.WithErrorHandler[int](recordLosses(t, events)),
					subscriber.WithOnDone[int](func() { events.record("done 1") }),
					subscriber.WithOnDone[int](nil),
					subscriber.WithOnDone[int](func() { events.record("done 2") }),
				}, tt.options...)
				id, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, msg int) error {
					events.record(fmt.Sprintf("handled %d", msg))
					<-hold

					return nil
				}, options...)
				if err != nil {
					t.Fatalf("Subscribe: %v", err)
				}

				// handle holds on to 1, and 2 fills the buffer.
				b.Broadcast(t.Context(), 1)
				b.Broadcast(t.Context(), 2)
				tt.remove(t, b, id, cancel)
				synctest.Wait()
				if got := events.messages(); slices.Contains(got, "done 1") {
					t.Fatalf("onDone ran while handle held on to 1: %q", got)
				}

				close(hold)
				synctest.Wait()
				if got := events.messages(); !slices.Equal(got, tt.want) {
					t.Errorf("got %q, want %q", got, tt.want)
				}
			})
		})
	}
}

// Shutdown waits for onDone, until its ctx is done.
func TestSubscriberWithOnDone_Shutdown(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		release := make(chan struct{})
		closed := false
		subscribe(t, b, func(context.Context, uuid.UUID, int) error { return nil },
			subscriber.WithOnDone[int](func() {
				<-release
				closed = true
			}))

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := b.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown while onDone runs = %v, want %v", err, context.DeadlineExceeded)
		}

		errs := shutdown(t.Context(), b)
		checkWaiting(t, errs, "onDone runs")
		close(release)
		if err := waitShutdown(t, errs); !errors.Is(err, broadcastor.ErrClosed) {
			t.Errorf("second Shutdown = %v, want ErrClosed", err)
		}
		// Without synctest.Wait: Shutdown returning is enough.
		if !closed {
			t.Error("Shutdown returned before onDone")
		}
	})
}

// For SubscribeSeq, onDone runs after the loop body's last call, however the loop ended, and never for a loop never
// ranged.
func TestSubscriberWithOnDone_SubscribeSeq(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		run  func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID, seq iter.Seq2[int, func(error)], events *recorder[string])
		want []string
	}{
		{"Break", func(t *testing.T, b *broadcastor.Broadcastor[int], _ uuid.UUID, seq iter.Seq2[int, func(error)], events *recorder[string]) {
			t.Helper()
			go func() {
				for msg := range seq {
					events.record(fmt.Sprintf("yielded %d", msg))

					break
				}
			}()
			b.Broadcast(t.Context(), 1)
		}, []string{"yielded 1", "done"}},
		{"Unsubscribe", func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID, seq iter.Seq2[int, func(error)], events *recorder[string]) {
			t.Helper()
			go func() {
				for msg := range seq {
					events.record(fmt.Sprintf("yielded %d", msg))
				}
			}()
			b.Broadcast(t.Context(), 1)
			unsubscribe(t, b, id)
		}, []string{"yielded 1", "done"}},
		{"NeverRanged", func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID, _ iter.Seq2[int, func(error)], _ *recorder[string]) {
			t.Helper()
			unsubscribe(t, b, id)
		}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := broadcastor.NewBroadcastor[int]()
				events := &recorder[string]{}
				id, seq := subscribeSeq(t, b, subscriber.WithOnDone[int](func() { events.record("done") }))
				tt.run(t, b, id, seq, events)
				synctest.Wait()
				if got := events.messages(); !slices.Equal(got, tt.want) {
					t.Errorf("got %q, want %q", got, tt.want)
				}
			})
		})
	}
}
