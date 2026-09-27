// One subscriber gets every message, in the order it was broadcast.
//
// handle runs in the subscriber's own goroutine, and Broadcast returns once the subscriber has taken the message, not
// once it has handled it. So main waits for handle before it returns.
package main

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	var handled sync.WaitGroup
	id, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, msg string) error {
		defer handled.Done()
		fmt.Println("got", msg)

		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	for _, msg := range []string{"hello", "world"} {
		handled.Add(1)
		b.Broadcast(ctx, msg)
	}
	handled.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
