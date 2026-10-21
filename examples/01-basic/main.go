// One subscriber gets every message, in publish order. handle gets the subscriber's ID, so it can unsubscribe
// itself, since Unsubscribe never waits. Publish then hands the message to nobody.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/elojah/multicast"
)

func main() {
	ctx := context.Background()
	g := multicast.New[string]()

	stopped := make(chan struct{})
	_, err := g.Subscribe(ctx, func(ctx context.Context, id uint64, msg string) error {
		fmt.Println("got", msg)
		if msg != "stop" {
			return nil
		}
		defer close(stopped)

		return g.Unsubscribe(ctx, id)
	})
	if err != nil {
		log.Fatal(err)
	}

	for _, msg := range []string{"hello", "world", "stop"} {
		g.Publish(ctx, msg)
	}
	// Publish returns once the subscriber has taken the message, not handled it.
	<-stopped
	fmt.Println("handed to", g.Publish(ctx, "anyone?"), "subscribers")
}
