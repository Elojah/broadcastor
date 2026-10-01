// Redis keeps what a Broadcastor must not lose when the process stops. A sorted set, scored by the time each message
// was broadcast, is the history (broadcastor.WithHistory) that a subscriber joining after a restart replays
// (subscriber.WithReplay), and a stream is the store.Queue that keeps the messages a subscriber loses
// (subscriber.WithStore), for store.Drain to hand them back.
//
// Here the alerts fail to page while the pager is down, and the process restarts. The alerts then page what they lost,
// and a dashboard joins, which replays the readings of both runs before the live ones.
//
// It is a module of its own, so that the library does not depend on go-redis: run it with
// `go run -C examples/23-redis .`. It uses the Redis at REDIS_ADDR, localhost:6379 by default, such as
// `docker run --rm -p 6379:6379 redis`, and deletes its two keys there first, so that it prints the same each time.
// Its test runs it against miniredis, in memory.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

const (
	historyKey     = "broadcastor:examples:redis:history"
	deadLettersKey = "broadcastor:examples:redis:dead-letters"
)

var errPagerDown = errors.New("pager unreachable")

type reading struct {
	Sensor  string    `json:"sensor"`
	At      time.Time `json:"at"`
	Celsius int       `json:"celsius"`
}

func (r reading) String() string {
	return fmt.Sprintf("%s at %s, %d°C", r.Sensor, r.At.Format(time.TimeOnly), r.Celsius)
}

func at(seconds, celsius int) reading {
	return reading{"north", time.Date(2026, 10, 1, 10, 0, seconds, 0, time.UTC), celsius}
}

func main() {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	run(addr)
}

func run(addr string) {
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Del(ctx, historyKey, deadLettersKey).Err(); err != nil {
		log.Fatal(err)
	}

	beforeRestart(ctx, client)
	afterRestart(ctx, client)

	if err := client.Close(); err != nil {
		log.Fatal(err)
	}
}

// beforeRestart broadcasts four readings, and the alerts lose the two they fail to page.
func beforeRestart(ctx context.Context, client *redis.Client) {
	b := broadcastor.NewBroadcastor(broadcastor.WithHistory(newHistory[reading](client, historyKey, time.Hour)))

	// Every reading is either handled or stored, then reported.
	var done sync.WaitGroup
	_, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, r reading) error {
		if r.Celsius > 30 {
			fmt.Println("alerts failed to page", r)

			return errPagerDown
		}
		done.Done()

		return nil
	},
		subscriber.WithStore(newDeadLetters[reading](client, deadLettersKey, 10_000)),
		subscriber.WithErrorHandler[reading](func(context.Context, error) { done.Done() }),
	)
	if err != nil {
		log.Fatal(err)
	}

	for _, r := range []reading{at(0, 20), at(1, 31), at(2, 22), at(3, 33)} {
		done.Add(1)
		b.Broadcast(ctx, r)
	}
	done.Wait()
	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
}

// afterRestart pages what the alerts lost, then a dashboard joins, which replays what was broadcast before it.
func afterRestart(ctx context.Context, client *redis.Client) {
	pageAgain(ctx, newDeadLetters[reading](client, deadLettersKey, 10_000))

	// A new Broadcastor, with a new history over the same key.
	b := broadcastor.NewBroadcastor(broadcastor.WithHistory(newHistory[reading](client, historyKey, time.Hour)))
	fmt.Println(at(4, 21), "handed to", b.Broadcast(ctx, at(4, 21)), "subscribers")

	var handled sync.WaitGroup
	handled.Add(6) // five replayed, one live
	_, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, r reading) error {
		fmt.Println("dashboard got", r)
		handled.Done()

		return nil
	}, subscriber.WithReplay[reading](nil))
	if err != nil {
		log.Fatal(err)
	}

	b.Broadcast(ctx, at(5, 34))
	handled.Wait()
	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
}

// pageAgain pages what the alerts lost, oldest first, now that the pager is back. Drain acks each, then waits for more,
// so pageAgain stops it once the queue is empty.
func pageAgain(ctx context.Context, lost *deadLetters[reading]) {
	left, err := lost.Len(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if left == 0 {
		return
	}
	paging, stop := context.WithCancel(ctx)
	defer stop()
	err = store.Drain(paging, lost, func(_ context.Context, _ uuid.UUID, r reading) error {
		fmt.Println("alerts paged", r)
		if left--; left == 0 {
			stop()
		}

		return nil
	}, nil)
	if !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
