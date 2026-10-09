// A non-blocking Broadcast never waits: a subscriber busy in handle, or with a full buffer, misses the message with a
// *subscriber.DroppedError.
//
// handle waits for release, standing in for slow work.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	release := make(chan struct{})
	var handled sync.WaitGroup
	id, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, msg string) error {
		<-release
		fmt.Println("got", msg)
		handled.Done()

		return nil
	}, subscriber.WithErrorHandler[string](func(_ context.Context, err error) {
		var dropped *subscriber.DroppedError[string]
		if errors.As(err, &dropped) {
			fmt.Println("dropped", dropped.Message)
		}
	}))
	if err != nil {
		log.Fatal(err)
	}

	handled.Add(1)
	b.Broadcast(ctx, "first") // the subscriber takes it, then waits in handle
	n := b.Broadcast(ctx, "second", message.WithNonBlocking[string]())
	fmt.Println("second handed to", n, "subscribers")
	close(release)
	handled.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
