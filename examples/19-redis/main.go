// A Redis stream is the store.Queue that keeps the messages a subscriber loses (subscriber.WithDeadLetters), so that
// they outlive the process, for store.Drain to hand them back. Another keeps the history of every message, handled
// (middleware.History) or lost.
//
// Here the alerts fail to page while the pager is down, and the process restarts. The alerts then page what they lost,
// and the history shows every reading, twice for those paged late.
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
	"sync"
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

	beforeRestart(ctx, client)
	// After the restart, with new streams over the same keys.
	history := newStream[reading](client, historyKey, maxLen)
	pageAgain(ctx, newStream[reading](client, deadLettersKey, maxLen), history)
	printHistory(ctx, history)

	if err := client.Close(); err != nil {
		log.Fatal(err)
	}
}

// beforeRestart broadcasts four readings, and the alerts lose the two they fail to page.
func beforeRestart(ctx context.Context, client *redis.Client) {
	b := broadcastor.NewBroadcastor[reading]()
	lost := newStream[reading](client, deadLettersKey, maxLen)
	history := newStream[reading](client, historyKey, maxLen)

	// Every reading is either handled and put in the history, or reported: lost, or handled but not put in the history.
	var done sync.WaitGroup
	handled := store.PutFunc[reading](func(ctx context.Context, r subscriber.Record[reading]) error {
		if err := history.Put(ctx, r); err != nil {
			return err
		}
		done.Done()

		return nil
	})
	_, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, r reading) error {
		if r.Celsius > 30 {
			fmt.Println("alerts failed to page", r)

			return errPagerDown
		}

		return nil
	},
		subscriber.WithMiddleware(middleware.History[reading](handled)),
		// The lost readings go in the history too, so that it has every one.
		subscriber.WithDeadLetters[reading](store.PutFunc[reading](func(ctx context.Context, r subscriber.Record[reading]) error {
			return errors.Join(lost.Put(ctx, r), history.Put(ctx, r))
		})),
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

// pageAgain pages what the alerts lost, oldest first, now that the pager is back, and puts each in the history. Drain
// acks each, then waits for more, so pageAgain stops it once the queue is empty.
func pageAgain(ctx context.Context, lost, history *stream[reading]) {
	left, err := lost.Len(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if left == 0 {
		return
	}
	paging, stop := context.WithCancel(ctx)
	defer stop()
	page := middleware.History[reading](history)(func(_ context.Context, _ uuid.UUID, r reading) error {
		fmt.Println("alerts paged", r)
		if left--; left == 0 {
			stop()
		}

		return nil
	})
	if err := store.Drain(paging, lost, page, nil); !errors.Is(err, context.Canceled) {
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
