package broadcastor_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

// benchSubscribers are the subscriber counts the Broadcast benchmarks run with.
var benchSubscribers = []int{1, 10, 100, 1000}

// benchBuffer is the buffer of every buffered subscriber in the benchmarks.
const benchBuffer = 64

// BenchmarkBroadcast measures one Broadcast until every subscriber has handled it, per delivery mode: mostly waking
// each parked goroutine. Waiting keeps async Broadcasts from piling up goroutines.
func BenchmarkBroadcast(b *testing.B) {
	modes := []struct {
		name      string
		subscribe []subscriber.Option[int]
		broadcast []message.Option[int]
	}{
		{name: "sync"},
		{name: "parallel", broadcast: []message.Option[int]{message.WithParallel[int]()}},
		{name: "async", broadcast: []message.Option[int]{message.WithAsync[int]()}},
		{name: "buffered", subscribe: []subscriber.Option[int]{subscriber.WithBuffer[int](benchBuffer)}},
		{name: "middleware", subscribe: []subscriber.Option[int]{subscriber.WithMiddleware(
			func(next subscriber.Handler[int]) subscriber.Handler[int] { return next },
		)}},
	}

	for _, subscribers := range benchSubscribers {
		for _, mode := range modes {
			b.Run(fmt.Sprintf("subscribers=%d/%s", subscribers, mode.name), func(b *testing.B) {
				bc := newBroadcastor[int](b)
				var handled sync.WaitGroup
				for range subscribers {
					_, err := bc.Subscribe(b.Context(), func(context.Context, uuid.UUID, int) error {
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
					bc.Broadcast(b.Context(), 0, mode.broadcast...)
					handled.Wait()
				}
			})
		}
	}
}

// BenchmarkBroadcast_Throughput measures Broadcast itself, per message a subscriber gets: subscribers are buffered, and
// waited for once, after the last Broadcast. It loops over b.N, since b.Loop would stop the timer before that wait.
func BenchmarkBroadcast_Throughput(b *testing.B) {
	modes := []struct {
		name      string
		broadcast []message.Option[int]
	}{
		{name: "sync"},
		{name: "parallel", broadcast: []message.Option[int]{message.WithParallel[int]()}},
		{name: "async", broadcast: []message.Option[int]{message.WithAsync[int]()}},
	}

	for _, subscribers := range benchSubscribers {
		for _, mode := range modes {
			b.Run(fmt.Sprintf("subscribers=%d/%s", subscribers, mode.name), func(b *testing.B) {
				bc := newBroadcastor[int](b)
				wait := subscribeCounting(b, bc, subscribers, b.N, subscriber.WithBuffer[int](benchBuffer))

				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					bc.Broadcast(b.Context(), 0, mode.broadcast...)
				}
				wait()
				reportPerDelivery(b, subscribers)
			})
		}
	}
}

// BenchmarkBroadcast_Concurrent is BenchmarkBroadcast_Throughput in sync mode from GOMAXPROCS goroutines, contending on
// each subscriber's references, counters and channel.
func BenchmarkBroadcast_Concurrent(b *testing.B) {
	for _, subscribers := range benchSubscribers {
		b.Run(fmt.Sprintf("subscribers=%d", subscribers), func(b *testing.B) {
			bc := newBroadcastor[int](b)
			// RunParallel shares the b.N Broadcasts out between its goroutines.
			wait := subscribeCounting(b, bc, subscribers, b.N, subscriber.WithBuffer[int](benchBuffer))

			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					bc.Broadcast(b.Context(), 0)
				}
			})
			wait()
			reportPerDelivery(b, subscribers)
		})
	}
}

// BenchmarkBroadcast_Churn is BenchmarkBroadcast_Throughput in sync mode while another goroutine subscribes and
// unsubscribes, so Broadcast ranges over a sync.Map being written. Only the steady subscribers count as deliveries, and
// the allocations include the churn's.
func BenchmarkBroadcast_Churn(b *testing.B) {
	for _, subscribers := range benchSubscribers {
		b.Run(fmt.Sprintf("subscribers=%d", subscribers), func(b *testing.B) {
			bc := newBroadcastor[int](b)
			wait := subscribeCounting(b, bc, subscribers, b.N, subscriber.WithBuffer[int](benchBuffer))

			stop := make(chan struct{})
			var churn sync.WaitGroup
			churn.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
					}
					// Buffered, so Broadcast never waits for one to wake up.
					if err := subscribeUnsubscribe(b.Context(), bc, subscriber.WithBuffer[int](benchBuffer)); err != nil {
						b.Error(err) // not Fatal, which must not be called from another goroutine

						return
					}
				}
			})

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				bc.Broadcast(b.Context(), 0)
			}
			wait()
			reportPerDelivery(b, subscribers)

			b.StopTimer()
			close(stop)
			churn.Wait()
		})
	}
}

// BenchmarkSubscribeUnsubscribe measures a subscription that comes and goes without ever getting a message.
func BenchmarkSubscribeUnsubscribe(b *testing.B) {
	bc := newBroadcastor[int](b)
	b.ReportAllocs()
	for b.Loop() {
		if err := subscribeUnsubscribe(b.Context(), bc); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSubscribeUnsubscribe_Parallel is BenchmarkSubscribeUnsubscribe from GOMAXPROCS goroutines at once, on the
// same Broadcastor.
func BenchmarkSubscribeUnsubscribe_Parallel(b *testing.B) {
	bc := newBroadcastor[int](b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := subscribeUnsubscribe(b.Context(), bc); err != nil {
				b.Error(err) // not Fatal, which must not be called from RunParallel's goroutines

				return
			}
		}
	})
}

func subscribeUnsubscribe(ctx context.Context, bc *broadcastor.Broadcastor[int], options ...subscriber.Option[int]) error {
	id, err := bc.Subscribe(ctx, func(context.Context, uuid.UUID, int) error { return nil }, options...)
	if err != nil {
		return fmt.Errorf("Subscribe: %w", err)
	}
	if err := bc.Unsubscribe(ctx, id); err != nil {
		return fmt.Errorf("Unsubscribe(%s): %w", id, err)
	}

	return nil
}

// subscribeCounting subscribes subscribers that each expect messages, and returns a func waiting until each has handled
// them. Each counts on its own, so waiting adds no contention. b.Context() unsubscribes them after each run.
func subscribeCounting(b *testing.B, bc *broadcastor.Broadcastor[int], subscribers, messages int, options ...subscriber.Option[int]) func() {
	b.Helper()
	done := make([]chan struct{}, subscribers)
	for i := range done {
		handled := make(chan struct{})
		done[i] = handled
		remaining := messages // only handle touches it, in the subscriber's goroutine
		_, err := bc.Subscribe(b.Context(), func(context.Context, uuid.UUID, int) error {
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

// reportPerDelivery reports the time per message a subscriber got, for b.N Broadcasts to subscribers each.
func reportPerDelivery(b *testing.B, subscribers int) {
	b.Helper()
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*subscribers), "ns/delivery")
}
