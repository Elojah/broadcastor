// One subscriber gets every message, in broadcast order. handle gets the subscriber's ID, so it can unsubscribe
// itself, since Unsubscribe never waits. Broadcast then hands the message to nobody.
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

	stopped := make(chan struct{})
	_, err := b.Subscribe(ctx, func(ctx context.Context, id uint64, msg string) error {
		fmt.Println("got", msg)
		if msg != "stop" {
			return nil
		}
		defer close(stopped)

		return b.Unsubscribe(ctx, id)
	})
	if err != nil {
		log.Fatal(err)
	}

	for _, msg := range []string{"hello", "world", "stop"} {
		b.Broadcast(ctx, msg)
	}
	// Broadcast returns once the subscriber has taken the message, not handled it.
	<-stopped
	fmt.Println("handed to", b.Broadcast(ctx, "anyone?"), "subscribers")
}
