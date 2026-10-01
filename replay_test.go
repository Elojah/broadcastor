package broadcastor_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

// A subscriber with subscriber.WithReplay handles the messages its Broadcastor's history kept, oldest first, then the
// live ones, and keep picks those it replays. One without it, or without a history, gets only the live ones.
func TestSubscriberWithReplay(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		history int
		options []subscriber.Option[int]
		want    []int
	}{
		{"All", 3, []subscriber.Option[int]{subscriber.WithReplay[int](nil)}, []int{3, 4, 5, 6}},
		{"Keep", 5, []subscriber.Option[int]{subscriber.WithReplay(func(msg int) bool { return msg%2 == 0 })}, []int{2, 4, 6}},
		{"NoReplay", 3, nil, []int{6}},
		{"NoHistory", 0, []subscriber.Option[int]{subscriber.WithReplay[int](nil)}, []int{6}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var options []broadcastor.Option[int]
				if tt.history > 0 {
					options = append(options, broadcastor.WithHistory(store.NewHistory[int](tt.history)))
				}
				b := broadcastor.NewBroadcastor(options...)
				for msg := 1; msg <= 5; msg++ {
					b.Broadcast(t.Context(), msg)
				}
				r := &recorder[int]{}
				id := subscribe(t, b, r.handle, tt.options...)
				if n := b.Broadcast(t.Context(), 6); n != 1 {
					t.Errorf("Broadcast(6) handed it to %d subscribers, want 1", n)
				}
				synctest.Wait()
				if got := r.messages(); !slices.Equal(got, tt.want) {
					t.Errorf("handled %v, want %v", got, tt.want)
				}
				n := uint64(len(tt.want))
				checkStats(t, b, id, subscriber.Stats{Delivered: n, Handled: n})

				unsubscribe(t, b, id)
				synctest.Wait()
			})
		})
	}
}

// With subscriber.WithOrder, the replayed messages are merged in order with the live ones.
func TestSubscriberWithReplay_Order(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor(broadcastor.WithHistory(store.NewHistory[int](8)))
		for _, msg := range []int{5, 3, 1} {
			b.Broadcast(t.Context(), msg)
		}
		r := &recorder[int]{}
		id := subscribe(t, b, r.handle,
			subscriber.WithReplay[int](nil),
			subscriber.WithOrder(subscriber.OrderPolicy[int]{Compare: cmp.Compare[int], Window: time.Second}),
		)
		for _, msg := range []int{4, 2} {
			b.Broadcast(t.Context(), msg)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got, want := r.messages(), []int{1, 2, 3, 4, 5}; !slices.Equal(got, want) {
			t.Errorf("handled %v, want %v", got, want)
		}

		unsubscribe(t, b, id)
		synctest.Wait()
	})
}

// A SubscribeSeq loop gets the replayed messages first, and once it breaks, those it did not get are reported as
// *subscriber.ClosedError.
func TestSubscriberWithReplay_Seq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor(broadcastor.WithHistory(store.NewHistory[int](8)))
		for msg := 1; msg <= 4; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		got, closed := &recorder[int]{}, &recorder[int]{}
		_, seq := subscribeSeq(t, b, subscriber.WithReplay[int](nil), subscriber.WithErrorHandler[int](recordClosed(t, closed)))
		for msg := range seq {
			got.record(msg)
			if msg == 2 {
				break
			}
		}
		synctest.Wait()
		if got, want := got.messages(), []int{1, 2}; !slices.Equal(got, want) {
			t.Errorf("the loop got %v, want %v", got, want)
		}
		if got, want := closed.messages(), []int{3, 4}; !slices.Equal(got, want) {
			t.Errorf("reported %v as closed, want %v", got, want)
		}
	})
}

// Unsubscribed with subscriber.WithUnsubscribeDiscard while replaying, a subscriber reports the rest of the history as
// *subscriber.ClosedError.
func TestSubscriberWithReplay_Discard(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor(broadcastor.WithHistory(store.NewHistory[int](8)))
		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		stuck, closed := &recorder[int]{hold: make(chan struct{})}, &recorder[int]{}
		id := subscribe(t, b, stuck.handle, subscriber.WithReplay[int](nil), subscriber.WithErrorHandler[int](recordClosed(t, closed)))
		synctest.Wait() // handle holds on to 1

		if err := b.Unsubscribe(t.Context(), id, subscriber.WithUnsubscribeDiscard()); err != nil {
			t.Fatalf("Unsubscribe: %v", err)
		}
		stuck.release()
		synctest.Wait()
		if got, want := stuck.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handled %v, want %v", got, want)
		}
		if got, want := closed.messages(), []int{2, 3}; !slices.Equal(got, want) {
			t.Errorf("reported %v as closed, want %v", got, want)
		}
	})
}

// A subscriber that joins while a Broadcast is appending waits for it, then replays that message rather than get it
// live, whatever order the History keeps them in.
func TestSubscriberWithReplay_WaitsForAppend(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		h := &slowHistory{History: store.NewHistory[int](8), slow: 2, release: make(chan struct{})}
		b := broadcastor.NewBroadcastor(broadcastor.WithHistory[int](h))
		b.Broadcast(t.Context(), 1)
		broadcasting := make(chan struct{})
		go func() {
			defer close(broadcasting)
			b.Broadcast(t.Context(), 2) // held up in Append
		}()
		synctest.Wait()

		r := &recorder[int]{}
		subscribed := make(chan uuid.UUID)
		go func() { subscribed <- subscribe(t, b, r.handle, subscriber.WithReplay[int](nil)) }()
		synctest.Wait() // Subscribe waits for 2's Append
		close(h.release)
		id := <-subscribed
		waitClosed(t, broadcasting, "Broadcast(2)")
		b.Broadcast(t.Context(), 3)
		synctest.Wait()
		if got, want := r.messages(), []int{1, 2, 3}; !slices.Equal(got, want) {
			t.Errorf("handled %v, want %v", got, want)
		}

		unsubscribe(t, b, id)
		synctest.Wait()
	})
}

// If Read fails, the error handler gets a *subscriber.ReplayError, and the subscriber handles only live messages.
func TestSubscriberWithReplay_ReadFails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor(broadcastor.WithHistory[int](&slowHistory{History: store.NewHistory[int](8), err: errHistory}))
		b.Broadcast(t.Context(), 1)
		r := &recorder[int]{}
		var reported []error
		id := subscribe(t, b, r.handle, subscriber.WithReplay[int](nil), subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
			reported = append(reported, err)
		}))
		b.Broadcast(t.Context(), 2)
		synctest.Wait()

		if got, want := r.messages(), []int{2}; !slices.Equal(got, want) {
			t.Errorf("handled %v, want %v", got, want)
		}
		var replay *subscriber.ReplayError
		if len(reported) != 1 || !errors.As(reported[0], &replay) || replay.SubscriberID != id ||
			!errors.Is(reported[0], subscriber.ErrReplay) || !errors.Is(reported[0], errHistory) {
			t.Errorf("error handler got %v, want a *ReplayError for %s wrapping %v", reported, id, errHistory)
		}

		unsubscribe(t, b, id)
		synctest.Wait()
	})
}

var errHistory = errors.New("history unavailable")

// slowHistory is a History whose Append holds up the message slow until release is closed, and whose Read fails with
// err if it is set.
type slowHistory struct {
	*store.History[int]

	slow    int
	release chan struct{}
	err     error
}

func (h *slowHistory) Append(ctx context.Context, offset uint64, msg int) {
	if msg == h.slow {
		<-h.release
	}
	h.History.Append(ctx, offset, msg)
}

func (h *slowHistory) Read(ctx context.Context) ([]subscriber.HistoryEntry[int], error) {
	if h.err != nil {
		return nil, h.err
	}

	return h.History.Read(ctx)
}

// However Subscribe races Broadcasts, a subscriber that replays the history gets each message once, either replayed
// or live, with none missed between the two. Each broadcaster broadcasts its own increasing numbers, which the history
// keeps in that order, so what a subscriber gets of each is all of it from some number on. It runs in real time, so
// that Broadcasts are under way while subscribers join.
func TestSubscriberWithReplay_ExactlyOnce(t *testing.T) {
	t.Parallel()

	const (
		broadcasters, perBroadcaster = 4, 500
		subscribers                  = 40
		history                      = 64
	)
	b := broadcastor.NewBroadcastor(broadcastor.WithHistory(store.NewHistory[int](history)))
	deliveries := []message.Option[int]{message.WithSync[int](), message.WithParallel[int](), message.WithAsync[int]()}
	var broadcasting sync.WaitGroup
	for g := range broadcasters {
		broadcasting.Go(func() {
			for i := range perBroadcaster {
				b.Broadcast(t.Context(), g*perBroadcaster+i, deliveries[randN(len(deliveries))])
			}
		})
	}

	type sub struct {
		id  uuid.UUID
		got *recorder[int]
	}
	subs := make([]sub, 0, subscribers)
	var loops sync.WaitGroup
	for i := range subscribers {
		got := &recorder[int]{}
		options := []subscriber.Option[int]{subscriber.WithReplay[int](nil), subscriber.WithBuffer[int](randN(4))}
		var (
			id  uuid.UUID
			seq iter.Seq[int]
			err error
		)
		if i%3 == 0 {
			id, seq, err = b.SubscribeSeq(context.WithoutCancel(t.Context()), options...)
			loops.Go(func() {
				for msg := range seq {
					got.record(msg)
				}
			})
		} else {
			id, err = b.Subscribe(context.WithoutCancel(t.Context()), got.handle, options...)
		}
		if err != nil {
			t.Fatalf("subscribing: %v", err)
		}
		subs = append(subs, sub{id: id, got: got})
		time.Sleep(time.Duration(randN(int(time.Millisecond))))
	}
	waitGroup(t, &broadcasting, "broadcasters")

	// Until every subscriber got the last number of every broadcaster, which comes last, but for async sends.
	deadline := time.Now().Add(deadlockTimeout)
	for _, s := range subs {
		for {
			got := s.got.messages()
			problem := replayProblem(got, broadcasters, perBroadcaster)
			if problem == "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("subscriber %s: %s", s.id, problem)
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitGroup(t, &loops, "SubscribeSeq loops")
}

// replayProblem describes what is wrong with what a subscriber got of the numbers broadcasters broadcast, each
// perBroadcaster of them: it must have got each once, and of each broadcaster's, all of them from some number on.
func replayProblem(got []int, broadcasters, perBroadcaster int) string {
	counts := make(map[int]int, len(got))
	for _, n := range got {
		counts[n]++
	}
	for g := range broadcasters {
		first := -1
		for i := range perBroadcaster {
			n := g*perBroadcaster + i
			switch c := counts[n]; {
			case c > 1:
				return fmt.Sprintf("got %d %d times", n, c)
			case c == 1 && first < 0:
				first = n
			case c == 0 && first >= 0:
				return fmt.Sprintf("got broadcaster %d's numbers from %d, but not %d", g, first, n)
			}
		}
		if first < 0 {
			return fmt.Sprintf("got none of broadcaster %d's numbers, among %d: %v", g, len(counts), first5(slices.Sorted(maps.Keys(counts))))
		}
	}

	return ""
}

func first5(values []int) []int {
	return values[:min(len(values), 5)]
}
