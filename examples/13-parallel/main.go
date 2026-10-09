// A parallel Broadcast sends to every subscriber at once and waits for all: a slow subscriber holds up nobody else, and
// messages stay in order, unlike with message.WithAsync.
//
// The slow subscriber's handle waits for release, which the fast one closes once it has got "second".
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
	slowID, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, msg string) error {
		<-release
		fmt.Println("slow got", msg)
		handled.Done()

		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	fastID, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, msg string) error {
		fmt.Println("fast got", msg)
		if msg == "second" {
			close(release)
		}
		handled.Done()

		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	handled.Add(4)
	b.Broadcast(ctx, "first") // the slow subscriber takes it, then waits in handle
	// Without message.WithParallel, this could wait forever: a sync Broadcast reaching the slow subscriber first waits
	// for it to finish "first", which waits for the fast one to get "second".
	n := b.Broadcast(ctx, "second", message.WithParallel[string]())
	handled.Wait()
	fmt.Println("second handed to", n, "subscribers")

	for _, id := range []uuid.UUID{slowID, fastID} {
		if err := b.Unsubscribe(ctx, id); err != nil {
			log.Fatal(err)
		}
	}
}
