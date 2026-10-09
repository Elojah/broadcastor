package broadcastor_test

import (
	"context"
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

// done gets the ID once, after handle's last call, however the subscriber was removed: once it has handled its buffer
// by default, or reported it with discard. With room in done, it gets the ID even once its ctx is done.
func TestSubscriberWithDone(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		evict  bool
		remove func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID, cancel context.CancelFunc)
		want   []string
	}{
		{"Unsubscribe", false, func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID, _ context.CancelFunc) {
			t.Helper()
			unsubscribe(t, b, id)
		}, []string{"handled 1", "handled 2"}},
		{"UnsubscribeDiscard", false, func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID, _ context.CancelFunc) {
			t.Helper()
			if err := b.Unsubscribe(t.Context(), id, subscriber.WithUnsubscribeDiscard()); err != nil {
				t.Fatalf("Unsubscribe: %v", err)
			}
		}, []string{"handled 1", "2 closed"}},
		{"Close", false, func(t *testing.T, b *broadcastor.Broadcastor[int], _ uuid.UUID, _ context.CancelFunc) {
			t.Helper()
			if err := b.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}, []string{"handled 1", "handled 2"}},
		{"ContextDone", false, func(t *testing.T, _ *broadcastor.Broadcastor[int], _ uuid.UUID, cancel context.CancelFunc) {
			t.Helper()
			cancel()
		}, []string{"handled 1", "handled 2"}},
		// 3 finds the buffer full and evicts the subscriber, which then discards 2.
		{"Evicted", true, func(t *testing.T, b *broadcastor.Broadcastor[int], _ uuid.UUID, _ context.CancelFunc) {
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
				dones := []chan uuid.UUID{make(chan uuid.UUID, 2), make(chan uuid.UUID, 2)}
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
					if got, want := received(done), []uuid.UUID{id}; !slices.Equal(got, want) {
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
		end  func(done <-chan uuid.UUID, cancel context.CancelFunc) []uuid.UUID
		want bool
	}{
		{"Room", 1, nil, true},
		{"Receiver", 0, func(done <-chan uuid.UUID, _ context.CancelFunc) []uuid.UUID {
			return []uuid.UUID{<-done}
		}, true},
		{"ContextDone", 0, func(_ <-chan uuid.UUID, cancel context.CancelFunc) []uuid.UUID {
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
				done := make(chan uuid.UUID, tt.room)
				id := subscribe(t, b, func(context.Context, uuid.UUID, int) error { return nil }, subscriber.WithDone[int](ctx, done))

				errs := shutdown(t.Context(), b)
				var got []uuid.UUID
				if tt.end != nil {
					checkWaiting(t, errs, "nothing received the ID")
					got = tt.end(done, cancel)
				}
				if err := waitShutdown(t, errs); err != nil {
					t.Fatalf("Shutdown = %v, want nil", err)
				}
				// Without synctest.Wait: Shutdown returning is enough.
				got = append(got, received(done)...)
				var want []uuid.UUID
				if tt.want {
					want = []uuid.UUID{id}
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
		done := make(chan uuid.UUID, 2) // one per subscriber
		handle := func(context.Context, uuid.UUID, int) error { return nil }
		ids := []uuid.UUID{
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
		run  func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID, seq iter.Seq2[int, func(error)], done <-chan uuid.UUID, events *recorder[string])
		want []string
	}{
		{"Break", func(t *testing.T, b *broadcastor.Broadcastor[int], _ uuid.UUID, seq iter.Seq2[int, func(error)], done <-chan uuid.UUID, events *recorder[string]) {
			t.Helper()
			go func() {
				for msg := range seq {
					events.record(fmt.Sprintf("yielded %d, %d done", msg, len(done)))

					break
				}
			}()
			b.Broadcast(t.Context(), 1)
		}, []string{"yielded 1, 0 done"}},
		{"Unsubscribe", func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID, seq iter.Seq2[int, func(error)], done <-chan uuid.UUID, events *recorder[string]) {
			t.Helper()
			go func() {
				for msg := range seq {
					events.record(fmt.Sprintf("yielded %d, %d done", msg, len(done)))
				}
			}()
			b.Broadcast(t.Context(), 1)
			unsubscribe(t, b, id)
		}, []string{"yielded 1, 0 done"}},
		{"NeverRanged", func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID, _ iter.Seq2[int, func(error)], _ <-chan uuid.UUID, _ *recorder[string]) {
			t.Helper()
			unsubscribe(t, b, id)
		}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := newBroadcastor[int](t)
				events := &recorder[string]{}
				done := make(chan uuid.UUID, 1)
				id, seq := subscribeSeq(t, b, subscriber.WithDone[int](t.Context(), done))
				tt.run(t, b, id, seq, done, events)
				synctest.Wait()
				if got := events.messages(); !slices.Equal(got, tt.want) {
					t.Errorf("got %q, want %q", got, tt.want)
				}
				var want []uuid.UUID
				if tt.want != nil {
					want = []uuid.UUID{id}
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
