// Close shuts the Broadcastor down: it unsubscribes every subscriber and waits until each has handled what it was sent,
// so main needs nothing else to wait for them. Every later Subscribe, Broadcast or Close fails with ErrClosed. From
// handle, Close has to be given handle's ctx, since it cannot wait for handle's own subscriber.
//
// The subscriber's buffer lets every Broadcast return before handle has run, so Close has messages to wait for.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/elojah/broadcastor"
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	if _, err := b.Subscribe(ctx, func(_ context.Context, msg string) error {
		fmt.Println("got", msg)

		return nil
	}, broadcastor.WithSubscriberBuffer[string](3)); err != nil {
		log.Fatal(err)
	}

	for _, msg := range []string{"one", "two", "three"} {
		if _, err := b.Broadcast(ctx, msg); err != nil {
			log.Fatal(err)
		}
	}
	if err := b.Close(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("closed")

	_, err := b.Broadcast(ctx, "four")
	fmt.Println("Broadcast after Close:", err)
}
