// A watchdog reads Stats.Handling to find a subscriber whose handle hangs, such as on a serial read, and unsubscribes
// it. Meanwhile subscriber.WithAsyncLimit bounds the async sends piling up behind it: past the limit, a message is
// dropped with a *subscriber.DroppedError. Stats.Sending shows how many wait.
//
// handle waits for release, standing in for a read that hangs.
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

const stuckAfter = 10 * time.Millisecond

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()
	names := map[uuid.UUID]string{}

	release := make(chan struct{})
	serial, err := b.Subscribe(ctx, func(context.Context, uuid.UUID, int) error {
		<-release

		return nil
	}, subscriber.WithAsyncLimit[int](2), subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
		var dropped *subscriber.DroppedError[int]
		if errors.As(err, &dropped) {
			fmt.Println("serial dropped", dropped.Message)
		}
	}))
	if err != nil {
		log.Fatal(err)
	}
	names[serial] = "serial"

	logger, err := b.Subscribe(ctx, func(context.Context, uuid.UUID, int) error {
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	names[logger] = "logger"

	b.Broadcast(ctx, 1) // serial takes it, then hangs in handle
	for n := 2; n <= 4; n++ {
		b.Broadcast(ctx, n, message.WithAsync[int]()) // 2 and 3 wait for serial, and 4 is dropped
	}
	time.Sleep(2 * stuckAfter)

	for _, s := range b.Stats() {
		if s.Handling < stuckAfter {
			fmt.Println(names[s.SubscriberID] + ": ok")

			continue
		}
		fmt.Printf("%s: stuck in handle for more than %v, %d async sends waiting, unsubscribed\n",
			names[s.SubscriberID], stuckAfter, s.Sending)
		// Frees the sends waiting on it, which then report their messages as *subscriber.ClosedError.
		if err := b.Unsubscribe(ctx, s.SubscriberID); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Println("subscribers left:", len(b.Stats()))

	close(release)
	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
}
