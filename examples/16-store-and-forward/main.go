// Store and forward: handle only puts each message in a store.Ring (store.Enqueue), and store.Drain forwards them to an
// uplink, retrying while it is down. Broadcast never waits for the uplink, which gets every message once, in order.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/store"
)

var errDown = errors.New("uplink down")

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()
	queue := store.NewRing[string](1024)
	if _, err := b.Subscribe(ctx, store.Enqueue[string](queue)); err != nil {
		log.Fatal(err)
	}

	var (
		up       atomic.Bool
		sent     sync.WaitGroup
		failed   = make(chan struct{})
		failOnce sync.Once
	)
	uplink := func(_ context.Context, _ uuid.UUID, msg string) error {
		if !up.Load() {
			failOnce.Do(func() { close(failed) })

			return errDown
		}
		fmt.Println("sent", msg)
		sent.Done()

		return nil
	}
	retry := middleware.Retry[string](middleware.RetryPolicy{Attempts: math.MaxInt, Delay: 10 * time.Millisecond})

	drainCtx, stop := context.WithCancel(ctx)
	drained := make(chan error, 1)
	go func() { drained <- store.Drain(drainCtx, queue, retry(uplink), nil) }()

	for _, msg := range []string{"a", "b", "c"} {
		sent.Add(1)
		b.Broadcast(ctx, msg)
		fmt.Println("broadcast", msg)
	}
	<-failed
	fmt.Println("uplink down, retrying")
	fmt.Println("uplink up")
	up.Store(true)
	sent.Wait()

	stop()
	fmt.Println("drain:", <-drained)
	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
}
