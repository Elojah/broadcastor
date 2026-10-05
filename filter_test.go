package broadcastor_test

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/subscriber"
)

// A subscriber skips the messages its filter rejects before Broadcast sends them: Broadcast does not wait for it, stuck
// as it is, nor count it, and the message is neither reported nor counted in Stats.
func TestSubscriberWithFilter(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		id := subscribe(t, b, stuck.handle, subscriber.WithFilter(even), subscriber.WithTimeout[int](time.Second),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
				t.Errorf("error handler got %v, want none", err)
			}))

		b.Broadcast(t.Context(), 2) // stuck holds on to 2
		start := time.Now()
		for _, msg := range []int{1, 3} {
			if n := b.Broadcast(t.Context(), msg); n != 0 {
				t.Errorf("Broadcast handed %d, which the filter rejects, to %d subscribers, want 0", msg, n)
			}
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Broadcasts of rejected messages returned after %v, want no wait", elapsed)
		}
		synctest.Wait()
		checkStats(t, b, id, subscriber.Stats{Delivered: 1})

		stuck.release()
		if n := b.Broadcast(t.Context(), 4); n != 1 {
			t.Errorf("Broadcast handed 4 to %d subscribers, want 1", n)
		}
		synctest.Wait()
		checkStats(t, b, id, subscriber.Stats{Delivered: 2, Handled: 2})
		unsubscribe(t, b, id)
		synctest.Wait()

		if got, want := stuck.messages(), []int{2, 4}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

// A message must pass every filter, called in order, so each one sees only what those before it kept. A nil filter
// keeps everything.
func TestSubscriberWithFilter_Several(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		seen := &recorder[int]{}
		r := &recorder[int]{}
		id := subscribe(t, b, r.handle,
			subscriber.WithFilter(even),
			subscriber.WithFilter[int](nil),
			subscriber.WithFilter(func(msg int) bool {
				seen.record(msg)

				return msg > 2
			}))

		for msg := 1; msg <= 6; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		unsubscribe(t, b, id)
		synctest.Wait()

		if got, want := seen.messages(), []int{2, 4, 6}; !slices.Equal(got, want) {
			t.Errorf("second filter got %v, want %v", got, want)
		}
		if got, want := r.messages(), []int{4, 6}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

func even(msg int) bool {
	return msg%2 == 0
}
