package broadcastor_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

// A Broadcast allocates nothing without options in sync, buffered and non-blocking modes, and once with options,
// whatever the number of subscribers. Not parallel: AllocsPerRun counts every goroutine's allocations.
func TestBroadcast_Allocs(t *testing.T) { //nolint:paralleltest // AllocsPerRun counts every goroutine's allocations
	const (
		subscribers = 10
		runs        = 100
		// Room for every message, so that no non-blocking send drops one.
		buffer = 2 * runs
	)
	for _, tt := range []struct { //nolint:paralleltest // AllocsPerRun counts every goroutine's allocations
		name      string
		subscribe []subscriber.Option[int]
		broadcast []message.Option[int]
		want      float64
	}{
		{name: "sync"},
		{name: "buffered", subscribe: []subscriber.Option[int]{subscriber.WithBuffer[int](buffer)}},
		{name: "non-blocking", subscribe: []subscriber.Option[int]{
			subscriber.WithBuffer[int](buffer), subscriber.WithDefaultMessageOptions(message.WithNonBlocking[int]()),
		}},
		{
			name:      "options",
			subscribe: []subscriber.Option[int]{subscriber.WithBuffer[int](buffer)},
			broadcast: []message.Option[int]{message.WithNonBlocking[int]()},
			want:      1,
		},
		{
			name:      "subscriber filter",
			subscribe: []subscriber.Option[int]{subscriber.WithBuffer[int](buffer)},
			broadcast: []message.Option[int]{message.WithSubscriberFilter[int](func(uuid.UUID) bool { return true })},
			want:      1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := newBroadcastor[int](t)
			for range subscribers {
				subscribe(t, b, func(context.Context, uuid.UUID, int) error { return nil }, tt.subscribe...)
			}

			got := testing.AllocsPerRun(runs, func() {
				if n := b.Broadcast(t.Context(), 0, tt.broadcast...); n != subscribers {
					t.Errorf("Broadcast handed the message to %d subscribers, want %d", n, subscribers)
				}
			})
			if got > tt.want {
				t.Errorf("Broadcast to %d subscribers allocates %v times, want at most %v", subscribers, got, tt.want)
			}

			if err := b.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		})
	}
}
