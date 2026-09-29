// Stats is a snapshot of every subscriber's counters. Here one subscriber is stuck in handle with a full buffer, so it
// misses a non-blocking message and one with a timeout, while another fails on every message.
//
// handle waits for release, standing in for slow work. HandleTime depends on the machine, so it is not printed.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

var errDown = errors.New("uplink down")

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()
	names := map[uuid.UUID]string{}

	release := make(chan struct{})
	slow, err := b.Subscribe(ctx, func(context.Context, uuid.UUID, int) error {
		<-release

		return nil
	}, subscriber.WithBuffer[int](2))
	if err != nil {
		log.Fatal(err)
	}
	names[slow] = "slow"

	// Failed is counted before the error handler is called.
	var failed sync.WaitGroup
	uplink, err := b.Subscribe(ctx, func(context.Context, uuid.UUID, int) error {
		return errDown
	}, subscriber.WithBuffer[int](8), subscriber.WithErrorHandler[int](func(context.Context, error) {
		failed.Done()
	}))
	if err != nil {
		log.Fatal(err)
	}
	names[uplink] = "uplink"

	failed.Add(5)
	for n := 1; n <= 3; n++ {
		b.Broadcast(ctx, n) // slow holds on to 1, and 2 and 3 fill its buffer
	}
	b.Broadcast(ctx, 4, message.WithNonBlocking[int]())
	b.Broadcast(ctx, 5, message.WithTimeout[int](10*time.Millisecond))
	failed.Wait()

	for _, s := range b.Stats() {
		fmt.Printf("%s: queued %d/%d, delivered %d, handled %d, failed %d, timed out %d, dropped %d\n",
			names[s.SubscriberID], s.Queued, s.Buffer, s.Delivered, s.Handled, s.Failed, s.TimedOut, s.Dropped)
	}

	close(release)
	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
}
