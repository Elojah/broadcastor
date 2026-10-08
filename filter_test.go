package broadcastor_test

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

// A filtered message never reaches the subscriber: Broadcast neither waits for it, stuck as it is, nor counts it, and
// it is neither reported nor in Stats.
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

// A message must pass every filter, in order, so each sees only what those before it kept. A nil filter keeps
// everything.
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

// A Broadcast hands its message only to the subscribers it picks: it neither waits for the others, stuck as they are,
// nor counts them, and nothing about them is reported or in their Stats.
func TestMessageWithSubscriberFilter(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		stuckID := subscribe(t, b, stuck.handle, subscriber.WithTimeout[int](time.Second),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
				t.Errorf("error handler got %v, want none", err)
			}))
		picked, pickedID := record(t, b)
		only := message.WithSubscriberFilter[int](func(id uuid.UUID) bool { return id == pickedID })

		b.Broadcast(t.Context(), 0) // stuck holds on to 0
		start := time.Now()
		for _, msg := range []int{1, 2} {
			if n := b.Broadcast(t.Context(), msg, only); n != 1 {
				t.Errorf("Broadcast handed %d to %d subscribers, want 1: the one picked", msg, n)
			}
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Broadcasts skipping the stuck subscriber returned after %v, want no wait", elapsed)
		}
		synctest.Wait()
		checkStats(t, b, stuckID, subscriber.Stats{Delivered: 1})

		stuck.release()
		unsubscribeAll(t, b, stuckID, pickedID)
		synctest.Wait()

		if got, want := stuck.messages(), []int{0}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber got %v, want %v", got, want)
		}
		if got, want := picked.messages(), []int{0, 1, 2}; !slices.Equal(got, want) {
			t.Errorf("picked subscriber got %v, want %v", got, want)
		}
	})
}

// A subscriber's own filters never see a message the Broadcast skips it for, so a stateful one only learns what the
// subscriber was handed.
func TestMessageWithSubscriberFilter_BeforeSubscriberFilter(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		seen := &recorder[int]{}
		r := &recorder[int]{}
		id := subscribe(t, b, r.handle, subscriber.WithFilter(func(msg int) bool {
			seen.record(msg)

			return true
		}))

		if n := b.Broadcast(t.Context(), 1, message.WithSubscriberFilter[int](func(uuid.UUID) bool { return false })); n != 0 {
			t.Errorf("Broadcast picking no subscriber handed the message to %d, want 0", n)
		}
		b.Broadcast(t.Context(), 2)
		unsubscribe(t, b, id)
		synctest.Wait()

		if got, want := seen.messages(), []int{2}; !slices.Equal(got, want) {
			t.Errorf("subscriber's filter got %v, want %v", got, want)
		}
		if got, want := r.messages(), []int{2}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

// A subscriber is picked only if every filter picks it, in order, so each sees only the subscribers those before it
// picked. A nil filter picks every one.
func TestMessageWithSubscriberFilter_Several(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		first, firstID := record(t, b)
		second, secondID := record(t, b)
		third, thirdID := record(t, b)
		// Only Broadcast's goroutine calls the filters.
		var seen []uuid.UUID

		n := b.Broadcast(t.Context(), 1,
			message.WithSubscriberFilter[int](func(id uuid.UUID) bool { return id != firstID }),
			message.WithSubscriberFilter[int](nil),
			message.WithSubscriberFilter[int](func(id uuid.UUID) bool {
				seen = append(seen, id)

				return id != secondID
			}))
		if n != 1 {
			t.Errorf("Broadcast handed the message to %d subscribers, want 1", n)
		}
		unsubscribeAll(t, b, firstID, secondID, thirdID)
		synctest.Wait()

		if len(seen) != 2 || !slices.Contains(seen, secondID) || !slices.Contains(seen, thirdID) {
			t.Errorf("last filter got %v, want %v and %v, in any order", seen, secondID, thirdID)
		}
		for _, tt := range []struct {
			name string
			r    *recorder[int]
			want []int
		}{
			{name: "first", r: first},
			{name: "second", r: second},
			{name: "third", r: third, want: []int{1}},
		} {
			if got := tt.r.messages(); !slices.Equal(got, tt.want) {
				t.Errorf("%s subscriber got %v, want %v", tt.name, got, tt.want)
			}
		}
	})
}

// A subscriber filter among a subscriber's default message options is ignored: only a Broadcast's own picks.
func TestSubscriberWithDefaultMessageOptions_SubscriberFilterIgnored(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		r := &recorder[int]{}
		id := subscribe(t, b, r.handle, subscriber.WithDefaultMessageOptions(
			message.WithSubscriberFilter[int](func(uuid.UUID) bool { return false }),
		))

		if n := b.Broadcast(t.Context(), 1); n != 1 {
			t.Errorf("Broadcast handed the message to %d subscribers, want 1", n)
		}
		unsubscribe(t, b, id)
		synctest.Wait()

		if got, want := r.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

func even(msg int) bool {
	return msg%2 == 0
}
