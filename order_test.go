package broadcastor_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

// A subscriber with subscriber.WithOrder holds what it takes for the window, then handles it in order, even when async
// sends arrive in any order. Broadcast does not wait while it holds them.
func TestSubscriberWithOrder(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		r := &recorder[int]{}
		id := subscribe(t, b, r.handle, subscriber.WithOrder(subscriber.OrderPolicy[int]{Compare: cmp.Compare[int], Window: time.Second}))

		for _, msg := range []int{3, 1, 4, 5, 2} {
			b.Broadcast(t.Context(), msg, message.WithAsync[int]())
		}
		synctest.Wait()
		if got := r.messages(); len(got) != 0 {
			t.Errorf("handled %v within the window, want nothing yet", got)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 5, Held: 5})

		time.Sleep(time.Second)
		synctest.Wait()
		if got, want := r.messages(), []int{1, 2, 3, 4, 5}; !slices.Equal(got, want) {
			t.Errorf("handled %v, want %v", got, want)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 5, Handled: 5})

		unsubscribe(t, b, id)
		synctest.Wait()
	})
}

// A message waits at most the window, from when the subscriber took it. When its window ends, the messages that sort
// before it are handled first, however recently they came.
func TestSubscriberWithOrder_Window(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		r := &recorder[int]{}
		id := subscribe(t, b, r.handle, subscriber.WithOrder(subscriber.OrderPolicy[int]{Compare: cmp.Compare[int], Window: time.Second}))

		b.Broadcast(t.Context(), 5)
		time.Sleep(time.Second / 2)
		b.Broadcast(t.Context(), 7)
		b.Broadcast(t.Context(), 1)
		time.Sleep(time.Second / 2) // 5's window ends
		synctest.Wait()
		if got, want := r.messages(), []int{1, 5}; !slices.Equal(got, want) {
			t.Errorf("once 5's window ended, handled %v, want %v", got, want)
		}

		time.Sleep(time.Second / 2) // 7's window ends
		synctest.Wait()
		if got, want := r.messages(), []int{1, 5, 7}; !slices.Equal(got, want) {
			t.Errorf("once 7's window ended, handled %v, want %v", got, want)
		}

		unsubscribe(t, b, id)
		synctest.Wait()
	})
}

// With no window, the messages queued while handle was busy are handled in order.
func TestSubscriberWithOrder_NoWindow(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		r := &recorder[int]{}
		release := make(chan struct{})
		id := subscribe(t, b, func(ctx context.Context, id uuid.UUID, msg int) error {
			if msg == 1 {
				<-release
			}

			return r.handle(ctx, id, msg)
		}, subscriber.WithBuffer[int](3), subscriber.WithOrder(subscriber.OrderPolicy[int]{Compare: cmp.Compare[int]}))

		b.Broadcast(t.Context(), 1)
		synctest.Wait() // handle holds on to 1
		for _, msg := range []int{4, 2, 3} {
			b.Broadcast(t.Context(), msg)
		}
		close(release)
		synctest.Wait()
		if got, want := r.messages(), []int{1, 2, 3, 4}; !slices.Equal(got, want) {
			t.Errorf("handled %v, want %v", got, want)
		}

		unsubscribe(t, b, id)
		synctest.Wait()
	})
}

// A slow handle holds Broadcast up as it would without subscriber.WithOrder: while a message is due, the subscriber
// takes no other, so it holds at most what was queued together instead of everything Broadcast can send.
func TestSubscriberWithOrder_SlowHandle(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		id := subscribe(t, b, func(context.Context, uuid.UUID, int) error {
			time.Sleep(time.Second)

			return nil
		}, subscriber.WithBuffer[int](1), subscriber.WithOrder(subscriber.OrderPolicy[int]{Compare: cmp.Compare[int]}))
		broadcasting := make(chan struct{})
		go func() {
			defer close(broadcasting)
			for msg := range 10 {
				b.Broadcast(t.Context(), msg)
			}
		}()

		time.Sleep(5*time.Second + time.Second/2)
		synctest.Wait()
		if s := statsOf(t, b, id); s.Held > 2 || s.Delivered >= 10 {
			t.Errorf("got Stats %+v after handling 5 messages, want at most 2 held, and Broadcast held up", s)
		}

		waitClosed(t, broadcasting, "Broadcasts")
		unsubscribe(t, b, id)
		time.Sleep(10 * time.Second) // until handle is done with the rest
		synctest.Wait()
	})
}

// Past the limit, the first message in order is handled before its window ends.
func TestSubscriberWithOrder_Limit(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		r := &recorder[int]{}
		id := subscribe(t, b, r.handle, subscriber.WithOrder(subscriber.OrderPolicy[int]{Compare: cmp.Compare[int], Window: time.Hour, Limit: 2}))

		for _, msg := range []int{3, 1, 2} {
			b.Broadcast(t.Context(), msg)
		}
		synctest.Wait()
		if got, want := r.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handled %v, want %v", got, want)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 3, Handled: 1, Held: 2})

		unsubscribe(t, b, id)
		synctest.Wait()
	})
}

// A message that sorts before one already handled is late: it is reported as a *subscriber.LateError, counted before
// that, and stored, once. One equal to it is not late.
func TestSubscriberWithOrder_Late(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		r := &recorder[int]{}
		reported := &recorder[string]{}
		stored := &recordStore{}
		var id uuid.UUID
		id = subscribe(t, b, r.handle,
			subscriber.WithOrder(subscriber.OrderPolicy[int]{Compare: cmp.Compare[int]}),
			subscriber.WithStore[int](stored),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
				var late *subscriber.LateError[int]
				if !errors.As(err, &late) || !errors.Is(err, subscriber.ErrLate) {
					t.Errorf("error handler got %v, want a *LateError", err)

					return
				}
				reported.record(fmt.Sprintf("%d late after %d, with Late at %d", late.Message, late.After, statsOf(t, b, id).Late))
			}),
		)

		for _, msg := range []int{2, 1, 2, 3} {
			b.Broadcast(t.Context(), msg)
			synctest.Wait() // so that each is handled before the next comes
		}
		if got, want := r.messages(), []int{2, 2, 3}; !slices.Equal(got, want) {
			t.Errorf("handled %v, want %v", got, want)
		}
		if got, want := reported.messages(), []string{"1 late after 2, with Late at 1"}; !slices.Equal(got, want) {
			t.Errorf("error handler got %q, want %q", got, want)
		}
		if records := stored.messages(); len(records) != 1 || records[0].Message != 1 || !errors.Is(records[0].Err, subscriber.ErrLate) {
			t.Errorf("store got %v, want 1 as late", records)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 4, Handled: 3, Late: 1})

		unsubscribe(t, b, id)
		synctest.Wait()
	})
}

// Once unsubscribed, a subscriber handles what it holds right away, in order, or reports it when discarding.
func TestSubscriberWithOrder_Unsubscribe(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name                    string
		options                 []subscriber.UnsubscribeOption
		wantHandled, wantClosed []int
	}{
		{"Deliver", nil, []int{1, 2, 3}, nil},
		{"Discard", []subscriber.UnsubscribeOption{subscriber.WithUnsubscribeDiscard()}, nil, []int{1, 2, 3}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := broadcastor.NewBroadcastor[int]()
				handled, closed := &recorder[int]{}, &recorder[int]{}
				id := subscribe(t, b, handled.handle,
					subscriber.WithOrder(subscriber.OrderPolicy[int]{Compare: cmp.Compare[int], Window: time.Hour}),
					subscriber.WithErrorHandler[int](recordClosed(t, closed)),
				)
				for _, msg := range []int{3, 1, 2} {
					b.Broadcast(t.Context(), msg)
				}
				synctest.Wait()

				start := time.Now()
				if err := b.Unsubscribe(t.Context(), id, tt.options...); err != nil {
					t.Fatalf("Unsubscribe: %v", err)
				}
				synctest.Wait()
				if waited := time.Since(start); waited != 0 {
					t.Errorf("the subscriber waited %v once unsubscribed, want 0", waited)
				}
				if got := handled.messages(); !slices.Equal(got, tt.wantHandled) {
					t.Errorf("handled %v, want %v", got, tt.wantHandled)
				}
				if got := closed.messages(); !slices.Equal(got, tt.wantClosed) {
					t.Errorf("reported %v as closed, want %v", got, tt.wantClosed)
				}
			})
		})
	}
}

// A SubscribeSeq loop gets its messages in order too, and once it breaks, what the subscriber still holds is reported
// as a *subscriber.ClosedError.
func TestSubscriberWithOrder_Seq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		got, closed := &recorder[int]{}, &recorder[int]{}
		_, seq := subscribeSeq(t, b,
			subscriber.WithOrder(subscriber.OrderPolicy[int]{Compare: cmp.Compare[int], Window: time.Second}),
			subscriber.WithErrorHandler[int](recordClosed(t, closed)),
		)
		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			for msg := range seq {
				got.record(msg)
				if msg == 2 {
					break
				}
			}
		}()

		for _, msg := range []int{3, 1, 4, 2} {
			b.Broadcast(t.Context(), msg)
		}
		time.Sleep(time.Second)
		waitClosed(t, loopDone, "SubscribeSeq loop ended by break")
		synctest.Wait()
		if got, want := got.messages(), []int{1, 2}; !slices.Equal(got, want) {
			t.Errorf("the loop got %v, want %v", got, want)
		}
		if got, want := closed.messages(), []int{3, 4}; !slices.Equal(got, want) {
			t.Errorf("reported %v as closed, want %v", got, want)
		}
	})
}

// subscriber.ByTime and subscriber.By order messages by one of their fields.
func TestSubscriberWithOrder_Compare(t *testing.T) {
	t.Parallel()

	type event struct {
		name string
		at   time.Duration
	}
	for _, tt := range []struct {
		name    string
		compare func(a, b event) int
		want    []string
	}{
		{"ByTime", subscriber.ByTime(func(e event) time.Time { return time.Unix(0, 0).Add(e.at) }), []string{"c", "b", "a"}},
		{"By", subscriber.By(func(e event) string { return e.name }), []string{"a", "b", "c"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := broadcastor.NewBroadcastor[event]()
				r := &recorder[event]{}
				id := subscribe(t, b, r.handle, subscriber.WithOrder(subscriber.OrderPolicy[event]{Compare: tt.compare, Window: time.Second}))
				for _, e := range []event{{"a", 2}, {"c", 0}, {"b", 1}} {
					b.Broadcast(t.Context(), e)
				}
				time.Sleep(time.Second)
				synctest.Wait()

				handled := r.messages()
				got := make([]string, 0, len(handled))
				for _, e := range handled {
					got = append(got, e.name)
				}
				if !slices.Equal(got, tt.want) {
					t.Errorf("handled %v, want %v", got, tt.want)
				}

				unsubscribe(t, b, id)
				synctest.Wait()
			})
		})
	}
}
