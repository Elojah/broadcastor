package broadcastor_test

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

func even(msg int) bool { return msg%2 == 0 }

// A subscriber gets only the messages its filter keeps, whether it subscribed with Subscribe or SubscribeSeq, and
// Broadcast counts only the subscribers that took the message.
func TestSubscriberWithFilter(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		all, allID := record(t, b)
		filtered := &recorder[int]{}
		filteredID := subscribe(t, b, filtered.handle, subscriber.WithFilter(even))
		seqID, seq := subscribeSeq(t, b, subscriber.WithFilter(even), subscriber.WithBuffer[int](4))

		var counts []int
		for msg := 1; msg <= 4; msg++ {
			counts = append(counts, b.Broadcast(t.Context(), msg))
		}
		unsubscribeAll(t, b, allID, filteredID, seqID)
		var pulled []int
		for msg := range seq {
			pulled = append(pulled, msg)
		}
		synctest.Wait()

		if want := []int{1, 3, 1, 3}; !slices.Equal(counts, want) {
			t.Errorf("Broadcast returned %v, want %v", counts, want)
		}
		if got, want := all.messages(), []int{1, 2, 3, 4}; !slices.Equal(got, want) {
			t.Errorf("unfiltered subscriber got %v, want %v", got, want)
		}
		if got, want := filtered.messages(), []int{2, 4}; !slices.Equal(got, want) {
			t.Errorf("filtered subscriber got %v, want %v", got, want)
		}
		if want := []int{2, 4}; !slices.Equal(pulled, want) {
			t.Errorf("filtered SubscribeSeq loop got %v, want %v", pulled, want)
		}
	})
}

// A message the filter rejects never holds Broadcast up, even for a busy subscriber, and is neither stored nor
// reported, whereas one it keeps is.
func TestSubscriberWithFilter_BusySubscriber(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		busy := &recorder[int]{hold: make(chan struct{})}
		stored := &recordStore{}
		reported := &recorder[error]{}
		id := subscribe(t, b, busy.handle,
			subscriber.WithFilter(even),
			subscriber.WithStore[int](stored),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) { reported.record(err) }),
		)
		b.Broadcast(t.Context(), 0) // busy is now handling 0 and not reading

		start := time.Now()
		if n := b.Broadcast(t.Context(), 1, message.WithTimeout[int](time.Second)); n != 0 {
			t.Errorf("Broadcast of a filtered message = %d, want 0", n)
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("Broadcast of a filtered message waited %v for a busy subscriber, want 0", waited)
		}
		b.Broadcast(t.Context(), 2, message.WithTimeout[int](time.Second))
		busy.release()
		unsubscribe(t, b, id)
		synctest.Wait()

		records := stored.messages()
		if len(records) != 1 || records[0].Message != 2 {
			t.Errorf("store got %v, want only message 2, which timed out", records)
		}
		if got := reported.messages(); len(got) != 1 {
			t.Errorf("error handler got %v, want only the timeout of message 2", got)
		}
	})
}
