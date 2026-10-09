// A Broadcast with a timeout gives up on a subscriber that does not take the message in time, with a
// *subscriber.TimeoutError. message.WithErrorHandler gives the message a handler of its own, and subscriber.WithTimeout
// sets a subscriber's default timeout.
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

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	release := make(chan struct{})
	id, err := b.Subscribe(ctx, func(context.Context, uuid.UUID, string) error {
		<-release

		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	b.Broadcast(ctx, "first") // the subscriber takes it, then waits in handle
	n := b.Broadcast(ctx, "second",
		message.WithTimeout[string](10*time.Millisecond),
		message.WithErrorHandler[string](func(_ context.Context, err error) {
			fmt.Println("timed out:", errors.Is(err, subscriber.ErrTimeout))
		}),
	)
	fmt.Println("second handed to", n, "subscribers")
	close(release)

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
