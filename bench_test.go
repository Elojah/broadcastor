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

// BenchmarkBroadcast measures one Broadcast until every subscriber has handled its message, with subscribers that do
// nothing, for each delivery mode. Waiting for every subscriber keeps async Broadcasts from piling up goroutines without
// bound. Idle subscribers are always ready to take the next message, so a buffer gains nothing here: it only helps with
// subscribers slower than the broadcaster.
func BenchmarkBroadcast(b *testing.B) {
	modes := []struct {
		name      string
		subscribe []subscriber.Option[int]
		broadcast []message.Option[int]
	}{
		{name: "sync"},
		{name: "parallel", broadcast: []message.Option[int]{message.WithParallel[int]()}},
		{name: "async", broadcast: []message.Option[int]{message.WithAsync[int]()}},
		{name: "buffered", subscribe: []subscriber.Option[int]{subscriber.WithBuffer[int](64)}},
		{name: "middleware", subscribe: []subscriber.Option[int]{subscriber.WithMiddleware(
			func(next subscriber.Handler[int]) subscriber.Handler[int] { return next },
		)}},
	}

	for _, subscribers := range []int{1, 10, 100, 1000} {
		for _, mode := range modes {
			b.Run(fmt.Sprintf("subscribers=%d/%s", subscribers, mode.name), func(b *testing.B) {
				bc := broadcastor.NewBroadcastor[int]()
				var handled sync.WaitGroup
				for range subscribers {
					// Not b.Context(), which is done before Cleanup runs, and would have unsubscribed them by then.
					id, err := bc.Subscribe(context.WithoutCancel(b.Context()), func(context.Context, uuid.UUID, int) error {
						handled.Done()

						return nil
					}, mode.subscribe...)
					if err != nil {
						b.Fatalf("Subscribe: %v", err)
					}
					b.Cleanup(func() {
						if err := bc.Unsubscribe(b.Context(), id); err != nil {
							b.Errorf("Unsubscribe(%s): %v", id, err)
						}
					})
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

// BenchmarkSubscribeUnsubscribe measures a subscription that comes and goes without ever getting a message.
func BenchmarkSubscribeUnsubscribe(b *testing.B) {
	bc := broadcastor.NewBroadcastor[int]()
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
	bc := broadcastor.NewBroadcastor[int]()
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

func subscribeUnsubscribe(ctx context.Context, bc *broadcastor.Broadcastor[int]) error {
	id, err := bc.Subscribe(ctx, func(context.Context, uuid.UUID, int) error { return nil })
	if err != nil {
		return fmt.Errorf("Subscribe: %w", err)
	}
	if err := bc.Unsubscribe(ctx, id); err != nil {
		return fmt.Errorf("Unsubscribe(%s): %w", id, err)
	}

	return nil
}
