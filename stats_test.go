package broadcastor_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

// Each counter counts what it says, while the subscriber is still subscribed.
func TestStats(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		// run subscribes to b and broadcasts 1, 2 and 3. It returns the subscriber's ID, and what lets its handle return
		// if it holds on to 1.
		run  func(t *testing.T, b *broadcastor.Broadcastor[int]) (uuid.UUID, func())
		want subscriber.Stats
		// released is what the Stats are once released, if run holds.
		released subscriber.Stats
	}{
		{"Handled", func(t *testing.T, b *broadcastor.Broadcastor[int]) (uuid.UUID, func()) {
			t.Helper()
			id := subscribe(t, b, func(context.Context, uuid.UUID, int) error {
				time.Sleep(time.Second)

				return nil
			})
			for msg := 1; msg <= 3; msg++ {
				b.Broadcast(t.Context(), msg)
			}
			time.Sleep(time.Second) // until handle is done with 3

			return id, nil
		}, subscriber.Stats{Delivered: 3, Handled: 3, HandleTime: 3 * time.Second}, subscriber.Stats{}},
		{"Failed", func(t *testing.T, b *broadcastor.Broadcastor[int]) (uuid.UUID, func()) {
			t.Helper()
			failed := &recorder[uint64]{}
			var id uuid.UUID
			id = subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
				if msg%2 == 1 {
					return handleError(msg)
				}

				return nil
			}, subscriber.WithErrorHandler[int](func(context.Context, error) {
				failed.record(statsOf(t, b, id).Failed)
			}))
			for msg := 1; msg <= 3; msg++ {
				b.Broadcast(t.Context(), msg)
			}
			synctest.Wait()
			// Counted before it is reported.
			if got, want := failed.messages(), []uint64{1, 2}; !slices.Equal(got, want) {
				t.Errorf("the error handler saw Failed at %v, want %v", got, want)
			}

			return id, nil
		}, subscriber.Stats{Delivered: 3, Handled: 1, Failed: 2}, subscriber.Stats{}},
		{"TimedOut", func(t *testing.T, b *broadcastor.Broadcastor[int]) (uuid.UUID, func()) {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, b, stuck.handle, subscriber.WithTimeout[int](time.Second))
			for msg := 1; msg <= 3; msg++ {
				b.Broadcast(t.Context(), msg) // stuck holds on to 1, so 2 and 3 time out
			}

			return id, stuck.release
		},
			subscriber.Stats{Delivered: 1, TimedOut: 2, Handling: 2 * time.Second},
			subscriber.Stats{Delivered: 1, Handled: 1, TimedOut: 2, HandleTime: 2 * time.Second},
		},
		{"Dropped", func(t *testing.T, b *broadcastor.Broadcastor[int]) (uuid.UUID, func()) {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, b, stuck.handle)
			b.Broadcast(t.Context(), 1) // stuck holds on to 1, so 2 and 3 are dropped
			b.Broadcast(t.Context(), 2, message.WithNonBlocking[int]())
			b.Broadcast(t.Context(), 3, message.WithNonBlocking[int]())

			return id, stuck.release
		},
			subscriber.Stats{Delivered: 1, Dropped: 2},
			subscriber.Stats{Delivered: 1, Handled: 1, Dropped: 2},
		},
		{"Handling", func(t *testing.T, b *broadcastor.Broadcastor[int]) (uuid.UUID, func()) {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, b, stuck.handle)
			b.Broadcast(t.Context(), 1) // stuck holds on to 1
			time.Sleep(time.Second)

			return id, stuck.release
		},
			subscriber.Stats{Delivered: 1, Handling: time.Second},
			subscriber.Stats{Delivered: 1, Handled: 1, HandleTime: time.Second},
		},
		{"Sending", func(t *testing.T, b *broadcastor.Broadcastor[int]) (uuid.UUID, func()) {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, b, stuck.handle)
			b.Broadcast(t.Context(), 1) // stuck holds on to 1, so 2 and 3 wait for it
			b.Broadcast(t.Context(), 2, message.WithAsync[int]())
			b.Broadcast(t.Context(), 3, message.WithAsync[int]())

			return id, stuck.release
		},
			subscriber.Stats{Sending: 2, Delivered: 1},
			subscriber.Stats{Delivered: 3, Handled: 3},
		},
		{"Queued", func(t *testing.T, b *broadcastor.Broadcastor[int]) (uuid.UUID, func()) {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, b, stuck.handle, subscriber.WithBuffer[int](2))
			for msg := 1; msg <= 3; msg++ {
				b.Broadcast(t.Context(), msg) // stuck holds on to 1, and 2 and 3 fill the buffer
			}

			return id, stuck.release
		},
			subscriber.Stats{Queued: 2, Buffer: 2, Delivered: 3},
			subscriber.Stats{Buffer: 2, Delivered: 3, Handled: 3},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := broadcastor.NewBroadcastor[int]()
				id, release := tt.run(t, b)
				synctest.Wait()
				checkStats(t, b, id, tt.want)
				if release != nil {
					release()
					synctest.Wait()
					checkStats(t, b, id, tt.released)
				}

				unsubscribe(t, b, id)
				synctest.Wait()
			})
		})
	}
}

// A SubscribeSeq loop body counts as handle.
func TestStats_SubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		id, seq := subscribeSeq(t, b)
		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			for range seq {
				time.Sleep(time.Second)
			}
		}()

		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		time.Sleep(time.Second) // until the loop body is done with 2
		synctest.Wait()
		checkStats(t, b, id, subscriber.Stats{Delivered: 2, Handled: 2, HandleTime: 2 * time.Second})

		unsubscribe(t, b, id)
		waitClosed(t, loopDone, "SubscribeSeq loop ended by Unsubscribe")
		synctest.Wait()
	})
}

// Stats holds every subscriber still subscribed, in the order they subscribed.
func TestStats_Subscribers(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		checkIDs := func(want []uuid.UUID, when string) {
			t.Helper()
			stats := b.Stats()
			got := make([]uuid.UUID, 0, len(stats))
			for _, s := range stats {
				got = append(got, s.SubscriberID)
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s, Stats are for subscribers %v, want %v", when, got, want)
			}
		}
		checkIDs(nil, "with no subscriber")

		ids := make([]uuid.UUID, 0, 5)
		for range cap(ids) {
			_, id := record(t, b)
			ids = append(ids, id)
		}
		checkIDs(ids, "once subscribed")

		unsubscribe(t, b, ids[1])
		checkIDs(slices.Delete(slices.Clone(ids), 1, 2), "after an Unsubscribe")

		if err := b.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		checkIDs(nil, "after Close")
		synctest.Wait()
	})
}

// However messages are sent, each one a Broadcast picks a subscriber up for is counted once, the losses are the errors
// reported, and HandleTime is the time spent in handle.
func TestStats_AddUp(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		var handleErrs, seqErrs atomic.Uint64
		options := func(errs *atomic.Uint64) []subscriber.Option[int] {
			return []subscriber.Option[int]{
				subscriber.WithBuffer[int](1),
				subscriber.WithErrorHandler[int](func(context.Context, error) { errs.Add(1) }),
			}
		}
		handleID := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			time.Sleep(time.Millisecond)
			if msg%3 == 0 {
				return handleError(msg)
			}

			return nil
		}, options(&handleErrs)...)
		seqID, seq := subscribeSeq(t, b, options(&seqErrs)...)
		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			for msg, fail := range seq {
				time.Sleep(time.Millisecond)
				if msg%3 == 0 {
					fail(handleError(msg))
				}
			}
		}()

		const broadcasters, perBroadcaster = 4, 50
		deliveries := []message.Option[int]{
			message.WithSync[int](), message.WithParallel[int](), message.WithAsync[int](), message.WithNonBlocking[int](),
		}
		var wg sync.WaitGroup
		for g := range broadcasters {
			wg.Go(func() {
				for i := range perBroadcaster {
					b.Broadcast(t.Context(), g*perBroadcaster+i, deliveries[i%len(deliveries)],
						message.WithTimeout[int](time.Millisecond))
				}
			})
		}
		waitGroup(t, &wg, "broadcasters")
		time.Sleep(time.Second) // until every async send and every handle is done
		synctest.Wait()

		stats := b.Stats()
		if len(stats) != 2 {
			t.Fatalf("Stats are for %d subscribers, want 2", len(stats))
		}
		for _, s := range stats {
			errs := map[uuid.UUID]*atomic.Uint64{handleID: &handleErrs, seqID: &seqErrs}[s.SubscriberID]
			if errs == nil {
				t.Fatalf("Stats for subscriber %s, want %s or %s", s.SubscriberID, handleID, seqID)
			}
			if s.Queued != 0 || s.Sending != 0 || s.Handling != 0 || s.Delivered != s.Handled+s.Failed {
				t.Errorf("subscriber %s: %+v, want Delivered = Handled + Failed, and nothing queued, sending or handling", s.SubscriberID, s)
			}
			if got := s.Handled + s.Failed + s.TimedOut + s.Dropped; got != broadcasters*perBroadcaster {
				t.Errorf("subscriber %s: %+v, counting %d messages, want %d", s.SubscriberID, s, got, broadcasters*perBroadcaster)
			}
			if lost, reported := s.Failed+s.TimedOut+s.Dropped, errs.Load(); lost != reported {
				t.Errorf("subscriber %s: %+v, counting %d losses, want the %d errors reported", s.SubscriberID, s, lost, reported)
			}
			if want := time.Duration(s.Handled+s.Failed) * time.Millisecond; s.HandleTime != want { //nolint:gosec // at most 200
				t.Errorf("subscriber %s: HandleTime is %v, want %v", s.SubscriberID, s.HandleTime, want)
			}
			if s.Handled == 0 || s.TimedOut+s.Dropped == 0 {
				t.Errorf("subscriber %s: %+v, want some messages handled and some lost", s.SubscriberID, s)
			}
		}

		if err := b.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		waitClosed(t, loopDone, "SubscribeSeq loop ended by Close")
		synctest.Wait()
	})
}

// statsOf returns the Stats of subscriber id. It may be called from any goroutine.
func statsOf[T any](t *testing.T, b *broadcastor.Broadcastor[T], id uuid.UUID) subscriber.Stats {
	t.Helper()
	for _, s := range b.Stats() {
		if s.SubscriberID == id {
			return s
		}
	}
	t.Errorf("no Stats for subscriber %s", id)

	return subscriber.Stats{}
}

// checkStats checks that subscriber id has the Stats want, but for its ID.
func checkStats[T any](t *testing.T, b *broadcastor.Broadcastor[T], id uuid.UUID, want subscriber.Stats) {
	t.Helper()
	want.SubscriberID = id
	if got := statsOf(t, b, id); got != want {
		t.Errorf("got Stats %+v, want %+v", got, want)
	}
}
