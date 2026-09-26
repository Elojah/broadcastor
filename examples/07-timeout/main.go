// A Broadcast with a timeout gives up on a subscriber that does not take the message in time, and tells the error
// handlers with a *TimeoutError. WithMessageErrorHandler gives the message a handler of its own, and
// WithSubscriberTimeout sets a timeout for every message sent to one subscriber.
//
// handle waits for release, standing in for slow work.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/elojah/broadcastor"
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	release := make(chan struct{})
	id, err := b.Subscribe(ctx, func(context.Context, string) error {
		<-release

		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	b.Broadcast(ctx, "first") // the subscriber takes it, then waits in handle
	n := b.Broadcast(ctx, "second",
		broadcastor.WithMessageTimeout[string](10*time.Millisecond),
		broadcastor.WithMessageErrorHandler[string](func(_ context.Context, err error) {
			fmt.Println("timed out:", errors.Is(err, broadcastor.ErrTimeout))
		}),
	)
	fmt.Println("second handed to", n, "subscribers")
	close(release)

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
