// handle can unsubscribe its subscriber, with the ID it gets, since Unsubscribe never waits. Broadcast then hands it
// nothing more.
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
	_, err := b.Subscribe(ctx, func(ctx context.Context, id uuid.UUID, msg string) error {
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

	b.Broadcast(ctx, "hello")
	b.Broadcast(ctx, "stop")
	<-stopped
	fmt.Println("handed to", b.Broadcast(ctx, "anyone?"), "subscribers")
}
