// A subscriber can unsubscribe itself from handle, since Unsubscribe never waits. Once it has, Broadcast no longer hands
// it anything.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	stopped := make(chan struct{})
	var id uuid.UUID
	id, err := b.Subscribe(ctx, func(ctx context.Context, msg string) error {
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

	for _, msg := range []string{"hello", "stop"} {
		if _, err := b.Broadcast(ctx, msg); err != nil {
			log.Fatal(err)
		}
	}
	<-stopped

	n, err := b.Broadcast(ctx, "anyone?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("handed to", n, "subscribers")
}
