// An async Broadcast returns right away, without waiting for a subscriber that is still busy: each subscriber is sent
// the message from a goroutine of its own. A slow subscriber then holds up nobody, but the messages of successive async
// Broadcasts can reach it out of order.
//
// handle waits for release, standing in for slow work.
package main

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/elojah/broadcastor"
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	release := make(chan struct{})
	var handled sync.WaitGroup
	id, err := b.Subscribe(ctx, func(_ context.Context, msg string) error {
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
	// Without WithMessageAsync, this would wait for handle, forever.
	b.Broadcast(ctx, "second", broadcastor.WithMessageAsync[string]())
	fmt.Println("both Broadcasts returned")
	close(release)
	handled.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
