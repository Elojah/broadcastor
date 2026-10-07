// An async Broadcast returns at once, sending to each subscriber from a goroutine of its own: a slow subscriber holds
// up nobody, but successive messages may reach it out of order.
//
// handle waits for release, standing in for slow work.
package main

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
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
	})
	if err != nil {
		log.Fatal(err)
	}

	handled.Add(2)
	b.Broadcast(ctx, "first") // the subscriber takes it, then waits in handle
	// Without message.WithAsync, this would wait for handle, forever.
	b.Broadcast(ctx, "second", message.WithAsync[string]())
	fmt.Println("both Broadcasts returned")
	close(release)
	handled.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
