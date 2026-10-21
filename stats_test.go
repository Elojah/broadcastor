package multicast_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elojah/multicast"
	"github.com/elojah/multicast/message"
	"github.com/elojah/multicast/subscriber"
)

// Each counter counts what it says, while the subscriber is still subscribed.
func TestStats(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		// run subscribes to g and publishes 1, 2 and 3. It returns the subscriber's ID, and what lets its handle return
		// if it holds on to 1.
		run  func(t *testing.T, g *multicast.Group[int]) (uint64, func())
		want subscriber.Stats
		// released is what the Stats are once released, if run holds.
		released subscriber.Stats
	}{
		{"Handled", func(t *testing.T, g *multicast.Group[int]) (uint64, func()) {
			t.Helper()
			id := subscribe(t, g, func(context.Context, uint64, int) error {
				time.Sleep(time.Second)

				return nil
			})
			for msg := 1; msg <= 3; msg++ {
				g.Publish(t.Context(), msg)
			}
			time.Sleep(time.Second) // until handle is done with 3

			return id, nil
		}, subscriber.Stats{Delivered: 3, Handled: 3, HandleTime: 3 * time.Second}, subscriber.Stats{}},
		{"Failed", func(t *testing.T, g *multicast.Group[int]) (uint64, func()) {
			t.Helper()
			failed := &recorder[uint64]{}
			var id uint64
			id = subscribe(t, g, func(_ context.Context, _ uint64, msg int) error {
				if msg%2 == 1 {
					return handleError(msg)
				}

				return nil
			}, subscriber.WithErrorHandler[int](func(context.Context, error) {
				failed.record(statsOf(t, g, id).Failed)
			}))
			for msg := 1; msg <= 3; msg++ {
				g.Publish(t.Context(), msg)
			}
			synctest.Wait()
			// Counted before it is reported.
			if got, want := failed.messages(), []uint64{1, 2}; !slices.Equal(got, want) {
				t.Errorf("the error handler saw Failed at %v, want %v", got, want)
			}

			return id, nil
		}, subscriber.Stats{Delivered: 3, Handled: 1, Failed: 2}, subscriber.Stats{}},
		{"TimedOut", func(t *testing.T, g *multicast.Group[int]) (uint64, func()) {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, g, stuck.handle, subscriber.WithTimeout[int](time.Second))
			for msg := 1; msg <= 3; msg++ {
				g.Publish(t.Context(), msg) // stuck holds on to 1, so 2 and 3 time out
			}

			return id, stuck.release
		},
			subscriber.Stats{Delivered: 1, TimedOut: 2, Handling: 2 * time.Second},
			subscriber.Stats{Delivered: 1, Handled: 1, TimedOut: 2, HandleTime: 2 * time.Second},
		},
		{"Dropped", func(t *testing.T, g *multicast.Group[int]) (uint64, func()) {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, g, stuck.handle)
			g.Publish(t.Context(), 1) // stuck holds on to 1, so 2 and 3 are dropped
			g.Publish(t.Context(), 2, message.WithNonBlocking[int]())
			g.Publish(t.Context(), 3, message.WithNonBlocking[int]())

			return id, stuck.release
		},
			subscriber.Stats{Delivered: 1, Dropped: 2},
			subscriber.Stats{Delivered: 1, Handled: 1, Dropped: 2},
		},
		{"Handling", func(t *testing.T, g *multicast.Group[int]) (uint64, func()) {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, g, stuck.handle)
			g.Publish(t.Context(), 1) // stuck holds on to 1
			time.Sleep(time.Second)

			return id, stuck.release
		},
			subscriber.Stats{Delivered: 1, Handling: time.Second},
			subscriber.Stats{Delivered: 1, Handled: 1, HandleTime: time.Second},
		},
		{"Sending", func(t *testing.T, g *multicast.Group[int]) (uint64, func()) {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, g, stuck.handle)
			g.Publish(t.Context(), 1) // stuck holds on to 1, so 2 and 3 wait for it
			g.Publish(t.Context(), 2, message.WithAsync[int]())
			g.Publish(t.Context(), 3, message.WithAsync[int]())

			return id, stuck.release
		},
			subscriber.Stats{Sending: 2, Delivered: 1},
			subscriber.Stats{Delivered: 3, Handled: 3},
		},
		{"Queued", func(t *testing.T, g *multicast.Group[int]) (uint64, func()) {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, g, stuck.handle, subscriber.WithBuffer[int](2))
			for msg := 1; msg <= 3; msg++ {
				g.Publish(t.Context(), msg) // stuck holds on to 1, and 2 and 3 fill the buffer
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
				g := newGroup[int](t)
				id, release := tt.run(t, g)
				synctest.Wait()
				checkStats(t, g, id, tt.want)
				if release != nil {
					release()
					synctest.Wait()
					checkStats(t, g, id, tt.released)
				}

				unsubscribe(t, g, id)
				synctest.Wait()
			})
		})
	}
}

// However sent, each message a Publish picks a subscriber up for is counted once, the losses match the errors
// reported, and HandleTime is the time spent in handle.
func TestStats_AddUp(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		g := newGroup[int](t)
		var handleErrs, seqErrs atomic.Uint64
		options := func(errs *atomic.Uint64) []subscriber.Option[int] {
			return []subscriber.Option[int]{
				subscriber.WithBuffer[int](1),
				subscriber.WithErrorHandler[int](func(context.Context, error) { errs.Add(1) }),
			}
		}
		handleID := subscribe(t, g, func(_ context.Context, _ uint64, msg int) error {
			time.Sleep(time.Millisecond)
			if msg%3 == 0 {
				return handleError(msg)
			}

			return nil
		}, options(&handleErrs)...)
		seqID, seq := subscribeSeq(t, g, options(&seqErrs)...)
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

		const publishers, perPublisher = 4, 50
		deliveries := []message.Option[int]{
			message.WithSync[int](), message.WithParallel[int](), message.WithAsync[int](), message.WithNonBlocking[int](),
		}
		var wg sync.WaitGroup
		for j := range publishers {
			wg.Go(func() {
				for i := range perPublisher {
					g.Publish(t.Context(), j*perPublisher+i, deliveries[i%len(deliveries)],
						message.WithTimeout[int](time.Millisecond))
				}
			})
		}
		waitGroup(t, &wg, "publishers")
		time.Sleep(time.Second) // until every async send and every handle is done
		synctest.Wait()

		stats := g.Stats()
		if len(stats) != 2 {
			t.Fatalf("Stats are for %d subscribers, want 2", len(stats))
		}
		for _, s := range stats {
			errs := map[uint64]*atomic.Uint64{handleID: &handleErrs, seqID: &seqErrs}[s.SubscriberID]
			if errs == nil {
				t.Fatalf("Stats for subscriber %d, want %d or %d", s.SubscriberID, handleID, seqID)
			}
			if s.Queued != 0 || s.Sending != 0 || s.Handling != 0 || s.Delivered != s.Handled+s.Failed {
				t.Errorf("subscriber %d: %+v, want Delivered = Handled + Failed, and nothing queued, sending or handling", s.SubscriberID, s)
			}
			if got := s.Handled + s.Failed + s.TimedOut + s.Dropped; got != publishers*perPublisher {
				t.Errorf("subscriber %d: %+v, counting %d messages, want %d", s.SubscriberID, s, got, publishers*perPublisher)
			}
			if lost, reported := s.Failed+s.TimedOut+s.Dropped, errs.Load(); lost != reported {
				t.Errorf("subscriber %d: %+v, counting %d losses, want the %d errors reported", s.SubscriberID, s, lost, reported)
			}
			if want := time.Duration(s.Handled+s.Failed) * time.Millisecond; s.HandleTime != want { //nolint:gosec // at most 200
				t.Errorf("subscriber %d: HandleTime is %v, want %v", s.SubscriberID, s.HandleTime, want)
			}
			if s.Handled == 0 || s.TimedOut+s.Dropped == 0 {
				t.Errorf("subscriber %d: %+v, want some messages handled and some lost", s.SubscriberID, s)
			}
		}

		if err := g.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		waitClosed(t, loopDone, "SubscribeSeq loop ended by Close")
		synctest.Wait()
	})
}

// statsOf returns the Stats of subscriber id. It may be called from any goroutine.
func statsOf[T any](t *testing.T, g *multicast.Group[T], id uint64) subscriber.Stats {
	t.Helper()
	for _, s := range g.Stats() {
		if s.SubscriberID == id {
			return s
		}
	}
	t.Errorf("no Stats for subscriber %d", id)

	return subscriber.Stats{}
}

// checkStats checks that subscriber id has the Stats want, but for its ID.
func checkStats[T any](t *testing.T, g *multicast.Group[T], id uint64, want subscriber.Stats) {
	t.Helper()
	want.SubscriberID = id
	if got := statsOf(t, g, id); got != want {
		t.Errorf("got Stats %+v, want %+v", got, want)
	}
}
