// A Redis stream keeps the messages a subscriber loses (subscriber.WithDeadLetters), so that they outlive the process,
// for subscriber.WithReplay to hand them back once it restarts. Another keeps the history of every message, handled
// (middleware.History) or lost.
//
// Here the alerts fail to page while the pager is down, and the process restarts. The alerts then page what they lost
// before any new reading, the stream of what they lost is left empty, and the history shows every reading, twice for
// those paged late.
//
// It is a module of its own, so that the library does not depend on go-redis: run it with
// `go run -C examples/19-redis .`. It uses the Redis at REDIS_ADDR, localhost:6379 by default (start one with
// `docker run --rm -p 6379:6379 redis`), and first deletes its keys there, so that it prints the same each time. Its
// test runs it against miniredis, in memory.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

const (
	deadLettersKey = "broadcastor:examples:redis:dead-letters"
	historyKey     = "broadcastor:examples:redis:history"
	maxLen         = 10_000
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
	if err := client.Del(ctx, deadLettersKey, historyKey).Err(); err != nil {
		log.Fatal(err)
	}

	// The same process twice: the pager is down before the restart, and back after it.
	runAlerts(ctx, client, true, at(0, 20), at(1, 31), at(2, 22), at(3, 33))
	runAlerts(ctx, client, false, at(4, 34))

	left, err := newStream[reading](client, deadLettersKey, maxLen).Len(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("lost and not paged yet:", left)
	printHistory(ctx, newStream[reading](client, historyKey, maxLen))

	if err := client.Close(); err != nil {
		log.Fatal(err)
	}
}

// runAlerts subscribes the alerts, which first page what they lost before, then broadcasts readings, and shuts down
// once the alerts have handled or lost each. They fail to page while pagerDown.
func runAlerts(ctx context.Context, client *redis.Client, pagerDown bool, readings ...reading) {
	b := broadcastor.NewBroadcastor[reading]()
	lost := newStream[reading](client, deadLettersKey, maxLen)
	history := newStream[reading](client, historyKey, maxLen)

	_, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, r reading) error {
		if r.Celsius <= 30 {
			return nil
		}
		if pagerDown {
			fmt.Println("alerts failed to page", r)

			return errPagerDown
		}
		fmt.Println("alerts paged", r)

		return nil
	},
		// Before any reading broadcast here. Each is acked once handled, or lost again and back in lost.
		subscriber.WithReplay(lost.Replay(ctx)),
		subscriber.WithMiddleware(middleware.History[reading](history)),
		// The lost readings go in the history too, so that it has every one.
		subscriber.WithDeadLetters[reading](store.PutFunc[reading](func(ctx context.Context, r subscriber.Record[reading]) error {
			return errors.Join(lost.Put(ctx, r), history.Put(ctx, r))
		})),
		// handle prints the others.
		subscriber.WithErrorHandler[reading](func(_ context.Context, err error) {
			if errors.Is(err, subscriber.ErrStore) {
				log.Println(err)
			}
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	for _, r := range readings {
		b.Broadcast(ctx, r)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := b.Shutdown(shutdownCtx); err != nil {
		log.Fatal(err)
	}
}

// printHistory prints every reading the alerts handled or lost, in the order they did, with the error if they lost it.
func printHistory(ctx context.Context, history *stream[reading]) {
	entries, err := history.All(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Err != nil {
			fmt.Printf("history: %s, lost: %v\n", entry.Message, entry.Err)

			continue
		}
		fmt.Println("history:", entry.Message)
	}
}
