package multicast_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/elojah/multicast"
	"github.com/elojah/multicast/message"
	"github.com/elojah/multicast/subscriber"
)

// A Publish allocates nothing without options in sync, buffered and non-blocking modes, and once with options,
// whatever the number of subscribers. Not parallel: AllocsPerRun counts every goroutine's allocations.
func TestPublish_Allocs(t *testing.T) { //nolint:paralleltest // AllocsPerRun counts every goroutine's allocations
	const (
		subscribers = 10
		runs        = 100
		// Room for every message, so that no non-blocking send drops one.
		buffer = 2 * runs
	)
	for _, tt := range []struct { //nolint:paralleltest // AllocsPerRun counts every goroutine's allocations
		name      string
		multicast []multicast.Option[int]
		subscribe []subscriber.Option[int]
		publish   []message.Option[int]
		want      float64
	}{
		{name: "sync"},
		{name: "serial", multicast: []multicast.Option[int]{multicast.WithSerial[int]()}},
		{name: "buffered", subscribe: []subscriber.Option[int]{subscriber.WithBuffer[int](buffer)}},
		{name: "non-blocking", subscribe: []subscriber.Option[int]{
			subscriber.WithBuffer[int](buffer), subscriber.WithDefaultMessageOptions(message.WithNonBlocking[int]()),
		}},
		{
			name:      "options",
			subscribe: []subscriber.Option[int]{subscriber.WithBuffer[int](buffer)},
			publish:   []message.Option[int]{message.WithNonBlocking[int]()},
			want:      1,
		},
		{
			name:      "subscriber filter",
			subscribe: []subscriber.Option[int]{subscriber.WithBuffer[int](buffer)},
			publish:   []message.Option[int]{message.WithSubscriberFilter[int](func(uint64) bool { return true })},
			want:      1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := newGroup(t, tt.multicast...)
			for range subscribers {
				subscribe(t, g, func(context.Context, uint64, int) error { return nil }, tt.subscribe...)
			}

			got := testing.AllocsPerRun(runs, func() {
				if n := g.Publish(t.Context(), 0, tt.publish...); n != subscribers {
					t.Errorf("Publish handed the message to %d subscribers, want %d", n, subscribers)
				}
			})
			if got > tt.want {
				t.Errorf("Publish to %d subscribers allocates %v times, want at most %v", subscribers, got, tt.want)
			}
		})
	}
}

// benchSubscribers are the subscriber counts the Publish benchmarks run with.
var benchSubscribers = []int{1, 10, 100, 1000}

// benchBuffer is the buffer of every buffered subscriber in the benchmarks.
const benchBuffer = 64

// BenchmarkPublish measures one Publish until every subscriber has handled it, per delivery mode: mostly waking
// each parked goroutine. Waiting keeps async Publish calls from piling up goroutines.
func BenchmarkPublish(b *testing.B) {
	modes := []struct {
		name      string
		subscribe []subscriber.Option[int]
		publish   []message.Option[int]
	}{
		{name: "sync"},
		{name: "parallel", publish: []message.Option[int]{message.WithParallel[int]()}},
		{name: "async", publish: []message.Option[int]{message.WithAsync[int]()}},
		{name: "buffered", subscribe: []subscriber.Option[int]{subscriber.WithBuffer[int](benchBuffer)}},
		{name: "middleware", subscribe: []subscriber.Option[int]{subscriber.WithMiddleware(
			func(next subscriber.Handler[int]) subscriber.Handler[int] { return next },
		)}},
	}

	for _, subscribers := range benchSubscribers {
		for _, mode := range modes {
			b.Run(fmt.Sprintf("subscribers=%d/%s", subscribers, mode.name), func(b *testing.B) {
				g := newGroup[int](b)
				var handled sync.WaitGroup
				for range subscribers {
					_, err := g.Subscribe(b.Context(), func(context.Context, uint64, int) error {
						handled.Done()

						return nil
					}, mode.subscribe...)
					if err != nil {
						b.Fatalf("Subscribe: %v", err)
					}
				}

				b.ReportAllocs()
				for b.Loop() {
					handled.Add(subscribers)
					g.Publish(b.Context(), 0, mode.publish...)
					handled.Wait()
				}
			})
		}
	}
}

// BenchmarkPublish_Throughput measures Publish itself, per message a subscriber gets: subscribers are buffered, and
// waited for once, after the last Publish. It loops over b.N, since b.Loop would stop the timer before that wait.
func BenchmarkPublish_Throughput(b *testing.B) {
	modes := []struct {
		name    string
		publish []message.Option[int]
	}{
		{name: "sync"},
		{name: "parallel", publish: []message.Option[int]{message.WithParallel[int]()}},
		{name: "async", publish: []message.Option[int]{message.WithAsync[int]()}},
	}

	for _, subscribers := range benchSubscribers {
		for _, mode := range modes {
			b.Run(fmt.Sprintf("subscribers=%d/%s", subscribers, mode.name), func(b *testing.B) {
				g := newGroup[int](b)
				wait := subscribeCounting(b, g, subscribers, b.N, subscriber.WithBuffer[int](benchBuffer))

				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					g.Publish(b.Context(), 0, mode.publish...)
				}
				wait()
				reportPerDelivery(b, subscribers)
			})
		}
	}
}

// BenchmarkPublishBatch_Throughput is BenchmarkPublish_Throughput in sync mode for batches of benchBatch values,
// per value a subscriber gets: handed one at a time to handle, or whole to a batch handler.
func BenchmarkPublishBatch_Throughput(b *testing.B) {
	const benchBatch = 16
	batch := batchOf(make([]int, benchBatch)...)

	for _, subscribers := range benchSubscribers {
		for _, whole := range []bool{false, true} {
			b.Run(fmt.Sprintf("subscribers=%d/batch handler=%t", subscribers, whole), func(b *testing.B) {
				g := newGroup[int](b)
				var wait func()
				if whole {
					wait = subscribeCountingBatches(b, g, subscribers, b.N, subscriber.WithBuffer[int](benchBuffer))
				} else {
					wait = subscribeCounting(b, g, subscribers, b.N*benchBatch, subscriber.WithBuffer[int](benchBuffer))
				}

				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					g.PublishBatch(b.Context(), batch)
				}
				wait()
				reportPerDelivery(b, subscribers*benchBatch)
			})
		}
	}
}

// BenchmarkPublish_Concurrent is BenchmarkPublish_Throughput in sync mode from GOMAXPROCS goroutines, contending on
// each subscriber's references, counters and channel.
func BenchmarkPublish_Concurrent(b *testing.B) {
	for _, subscribers := range benchSubscribers {
		b.Run(fmt.Sprintf("subscribers=%d", subscribers), func(b *testing.B) {
			g := newGroup[int](b)
			// RunParallel shares the b.N Publish calls out between its goroutines.
			wait := subscribeCounting(b, g, subscribers, b.N, subscriber.WithBuffer[int](benchBuffer))

			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					g.Publish(b.Context(), 0)
				}
			})
			wait()
			reportPerDelivery(b, subscribers)
		})
	}
}

// BenchmarkSubscribeUnsubscribe measures a subscription that comes and goes without ever getting a message.
func BenchmarkSubscribeUnsubscribe(b *testing.B) {
	g := newGroup[int](b)
	b.ReportAllocs()
	for b.Loop() {
		if err := subscribeUnsubscribe(b.Context(), g); err != nil {
			b.Fatal(err)
		}
	}
}

func subscribeUnsubscribe(ctx context.Context, g *multicast.Group[int], options ...subscriber.Option[int]) error {
	id, err := g.Subscribe(ctx, func(context.Context, uint64, int) error { return nil }, options...)
	if err != nil {
		return fmt.Errorf("Subscribe: %w", err)
	}
	if err := g.Unsubscribe(ctx, id); err != nil {
		return fmt.Errorf("Unsubscribe(%d): %w", id, err)
	}

	return nil
}

// subscribeCounting subscribes subscribers that each expect messages, and returns a func waiting until each has handled
// them. Each counts on its own, so waiting adds no contention. b.Context() unsubscribes them after each run.
func subscribeCounting(b *testing.B, g *multicast.Group[int], subscribers, messages int, options ...subscriber.Option[int]) func() {
	b.Helper()
	done := make([]chan struct{}, subscribers)
	for i := range done {
		handled := make(chan struct{})
		done[i] = handled
		remaining := messages // only handle touches it, in the subscriber's goroutine
		_, err := g.Subscribe(b.Context(), func(context.Context, uint64, int) error {
			remaining--
			if remaining == 0 {
				close(handled)
			}

			return nil
		}, options...)
		if err != nil {
			b.Fatalf("Subscribe: %v", err)
		}
	}

	return func() {
		for _, handled := range done {
			<-handled
		}
	}
}

// subscribeCountingBatches is subscribeCounting with batch handlers expecting batches.
func subscribeCountingBatches(b *testing.B, g *multicast.Group[int], subscribers, batches int, options ...subscriber.Option[int]) func() {
	b.Helper()
	done := make([]chan struct{}, subscribers)
	for i := range done {
		handled := make(chan struct{})
		done[i] = handled
		remaining := batches // only the batch handler touches it, in the subscriber's goroutine
		_, err := g.Subscribe(b.Context(), func(context.Context, uint64, int) error { return nil },
			append(options, subscriber.WithBatchHandler(func(context.Context, uint64, message.Batch[int]) error {
				remaining--
				if remaining == 0 {
					close(handled)
				}

				return nil
			}))...)
		if err != nil {
			b.Fatalf("Subscribe: %v", err)
		}
	}

	return func() {
		for _, handled := range done {
			<-handled
		}
	}
}

// reportPerDelivery reports the time per message a subscriber got, for b.N Publish calls of a message to deliveries,
// such as subscribers each.
func reportPerDelivery(b *testing.B, deliveries int) {
	b.Helper()
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*deliveries), "ns/delivery")
}
