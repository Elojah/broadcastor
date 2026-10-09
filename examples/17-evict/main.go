// subscriber.WithEvict unsubscribes a subscriber on the first error its evict picks. A sensor, unplugged after reading
// 2, fails on every later one, and failedInARow, our own middleware, turns its third failure in a row into errInARow,
// which evict picks. A stuck subscriber is evicted on its first timeout. Their error handlers get the eviction as a
// *subscriber.EvictedError.
//
// handle waits for release, standing in for slow work.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

var (
	errUnplugged = errors.New("unplugged")
	errInARow    = errors.New("failed in a row")
)

// failedInARow returns errInARow, wrapping the handler's error, once it has failed n times in a row. It needs no lock:
// only the subscriber's goroutine calls it, one message at a time.
func failedInARow(n int) subscriber.Middleware[int] {
	return func(next subscriber.Handler[int]) subscriber.Handler[int] {
		failures := 0

		return func(ctx context.Context, id uuid.UUID, reading int) error {
			err := next(ctx, id, reading)
			if err == nil {
				failures = 0

				return nil
			}
			if failures++; failures >= n {
				return fmt.Errorf("%w: %w", errInARow, err)
			}

			return err
		}
	}
}

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()

	// Its buffer has room for every message, so Broadcast never waits for it.
	_, err := b.Subscribe(ctx, func(context.Context, uuid.UUID, int) error {
		return nil
	}, subscriber.WithBuffer[int](8))
	if err != nil {
		log.Fatal(err)
	}

	sensorDone := make(chan uuid.UUID, 1)
	_, err = b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, reading int) error {
		if reading > 2 {
			return fmt.Errorf("reading %d: %w", reading, errUnplugged)
		}

		return nil
	}, subscriber.WithMiddleware(failedInARow(3)), subscriber.WithEvict[int](func(err error) bool {
		return errors.Is(err, errInARow)
	}), subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
		var evicted *subscriber.EvictedError[int]
		if errors.As(err, &evicted) {
			fmt.Println("sensor evicted:", evicted.Err)
		} else {
			fmt.Println("sensor failed:", err)
		}
	}), subscriber.WithDone[int](ctx, sensorDone))
	if err != nil {
		log.Fatal(err)
	}

	for reading := 1; reading <= 5; reading++ {
		b.Broadcast(ctx, reading)
	}
	<-sensorDone // evicted in its own goroutine

	release := make(chan struct{})
	_, err = b.Subscribe(ctx, func(context.Context, uuid.UUID, int) error {
		<-release

		return nil
	}, subscriber.WithEvict[int](func(err error) bool {
		return errors.Is(err, subscriber.ErrTimeout)
	}), subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
		var timeout *subscriber.TimeoutError[int]
		if errors.As(err, &timeout) {
			fmt.Printf("stuck lost %d, evicted: %t\n", timeout.Message, errors.Is(err, subscriber.ErrEvicted))
		}
	}))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("6 handed to", b.Broadcast(ctx, 6), "subscribers") // stuck takes it, then waits in handle
	for n := 7; n <= 8; n++ {
		fmt.Println(n, "handed to", b.Broadcast(ctx, n, message.WithTimeout[int](10*time.Millisecond)), "subscribers")
	}
	fmt.Println("subscribers left:", len(b.Stats()))

	close(release)
	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
}
