package broadcastor_test

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/subscriber"
)

// done gets the ID once, after handle's last call, however the subscriber was removed: once it has handled its buffer
// by default, or reported it with discard. With room in done, it gets the ID even once its ctx is done.
func TestSubscriberWithDone(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		evict  bool
		remove func(t *testing.T, b *broadcastor.Broadcastor[int], id uint64, cancel context.CancelFunc)
		want   []string
	}{
		{"Unsubscribe", false, func(t *testing.T, b *broadcastor.Broadcastor[int], id uint64, _ context.CancelFunc) {
			t.Helper()
			unsubscribe(t, b, id)
		}, []string{"handled 1", "handled 2"}},
		{"UnsubscribeDiscard", false, func(t *testing.T, b *broadcastor.Broadcastor[int], id uint64, _ context.CancelFunc) {
			t.Helper()
			if err := b.Unsubscribe(t.Context(), id, subscriber.WithUnsubscribeDiscard()); err != nil {
				t.Fatalf("Unsubscribe: %v", err)
			}
		}, []string{"handled 1", "2 closed"}},
		{"Close", false, func(t *testing.T, b *broadcastor.Broadcastor[int], _ uint64, _ context.CancelFunc) {
			t.Helper()
			if err := b.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}, []string{"handled 1", "handled 2"}},
		{"ContextDone", false, func(t *testing.T, _ *broadcastor.Broadcastor[int], _ uint64, cancel context.CancelFunc) {
			t.Helper()
			cancel()
		}, []string{"handled 1", "handled 2"}},
		// 3 finds the buffer full and evicts the subscriber, which then discards 2.
		{"Evicted", true, func(t *testing.T, b *broadcastor.Broadcastor[int], _ uint64, _ context.CancelFunc) {
			t.Helper()
			b.Broadcast(t.Context(), 3)
		}, []string{"handled 1", "3 evicted, timed out", "2 closed"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := newBroadcastor[int](t)
				events := &recorder[string]{}
				hold := make(chan struct{})
				ctx, cancel := context.WithCancel(subscribeCtx(t))
				defer cancel()
				// Room for two, so that a second send would show.
				dones := []chan uint64{make(chan uint64, 2), make(chan uint64, 2)}
				options := []subscriber.Option[int]{
					subscriber.WithBuffer[int](1),
					subscriber.WithErrorHandler[int](recordLosses(t, events)),
					subscriber.WithDone[int](ctx, dones[0]),
					subscriber.WithDone[int](ctx, nil),
					subscriber.WithDone[int](ctx, dones[1]),
				}
				if tt.evict {
					options = append(options, subscriber.WithEvict[int](isTimeout), subscriber.WithTimeout[int](time.Second))
				}
				id, err := b.Subscribe(ctx, func(_ context.Context, _ uint64, msg int) error {
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
				for _, done := range dones {
					if len(done) != 0 {
						t.Fatalf("done got the ID while handle held on to 1: %q", events.messages())
					}
				}

				close(hold)
				synctest.Wait()
				if got := events.messages(); !slices.Equal(got, tt.want) {
					t.Errorf("got %q, want %q", got, tt.want)
				}
				for _, done := range dones {
					if got, want := received(done), []uint64{id}; !slices.Equal(got, want) {
						t.Errorf("done got %v, want %v", got, want)
					}
				}
			})
		})
	}
}

// Shutdown returns once done has received the ID, or once WithDone's ctx is done: until then, it waits for a receiver.
func TestSubscriberWithDone_Shutdown(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		room int
		// end frees a Shutdown waiting on the send, and returns what done got.
		end  func(done <-chan uint64, cancel context.CancelFunc) []uint64
		want bool
	}{
		{"Room", 1, nil, true},
		{"Receiver", 0, func(done <-chan uint64, _ context.CancelFunc) []uint64 {
			return []uint64{<-done}
		}, true},
		{"ContextDone", 0, func(_ <-chan uint64, cancel context.CancelFunc) []uint64 {
			cancel()

			return nil
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := newBroadcastor[int](t)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan uint64, tt.room)
				id := subscribe(t, b, func(context.Context, uint64, int) error { return nil }, subscriber.WithDone[int](ctx, done))

				errs := shutdown(t.Context(), b)
				var got []uint64
				if tt.end != nil {
					checkWaiting(t, errs, "nothing received the ID")
					got = tt.end(done, cancel)
				}
				if err := waitShutdown(t, errs); err != nil {
					t.Fatalf("Shutdown = %v, want nil", err)
				}
				// Without synctest.Wait: Shutdown returning is enough.
				got = append(got, received(done)...)
				var want []uint64
				if tt.want {
					want = []uint64{id}
				}
				if !slices.Equal(got, want) {
					t.Errorf("done got %v once Shutdown returned, want %v", got, want)
				}
			})
		})
	}
}

// Subscribers can share done: each sends its own ID.
func TestSubscriberWithDone_Shared(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		done := make(chan uint64, 2) // one per subscriber
		handle := func(context.Context, uint64, int) error { return nil }
		ids := []uint64{
			subscribe(t, b, handle, subscriber.WithDone[int](t.Context(), done)),
			subscribe(t, b, handle, subscriber.WithDone[int](t.Context(), done)),
		}
		unsubscribeAll(t, b, ids...)
		synctest.Wait()

		got := received(done)
		if len(got) != len(ids) || !slices.Contains(got, ids[0]) || !slices.Contains(got, ids[1]) {
			t.Errorf("done got %v, want %v in any order", got, ids)
		}
	})
}

// For SubscribeSeq, done gets the ID after the loop body's last call, however the loop ended, and not for a loop until
// it is ranged.
func TestSubscriberWithDone_SubscribeSeq(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		run  func(t *testing.T, b *broadcastor.Broadcastor[int], id uint64, seq iter.Seq2[int, func(error)], done <-chan uint64, events *recorder[string])
		want []string
	}{
		{"Break", func(t *testing.T, b *broadcastor.Broadcastor[int], _ uint64, seq iter.Seq2[int, func(error)], done <-chan uint64, events *recorder[string]) {
			t.Helper()
			go func() {
				for msg := range seq {
					events.record(fmt.Sprintf("yielded %d, %d done", msg, len(done)))

					break
				}
			}()
			b.Broadcast(t.Context(), 1)
		}, []string{"yielded 1, 0 done"}},
		{"Unsubscribe", func(t *testing.T, b *broadcastor.Broadcastor[int], id uint64, seq iter.Seq2[int, func(error)], done <-chan uint64, events *recorder[string]) {
			t.Helper()
			go func() {
				for msg := range seq {
					events.record(fmt.Sprintf("yielded %d, %d done", msg, len(done)))
				}
			}()
			b.Broadcast(t.Context(), 1)
			unsubscribe(t, b, id)
		}, []string{"yielded 1, 0 done"}},
		{"NeverRanged", func(t *testing.T, b *broadcastor.Broadcastor[int], id uint64, _ iter.Seq2[int, func(error)], _ <-chan uint64, _ *recorder[string]) {
			t.Helper()
			unsubscribe(t, b, id)
		}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := newBroadcastor[int](t)
				events := &recorder[string]{}
				done := make(chan uint64, 1)
				id, seq := subscribeSeq(t, b, subscriber.WithDone[int](t.Context(), done))
				tt.run(t, b, id, seq, done, events)
				synctest.Wait()
				if got := events.messages(); !slices.Equal(got, tt.want) {
					t.Errorf("got %q, want %q", got, tt.want)
				}
				var want []uint64
				if tt.want != nil {
					want = []uint64{id}
				}
				if got := received(done); !slices.Equal(got, want) {
					t.Errorf("done got %v, want %v", got, want)
				}
				// Else the Broadcastor waits for it until the bubble ends. Unsubscribed, it ends at once.
				for range seq {
				}
			})
		})
	}
}
