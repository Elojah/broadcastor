// Every subscriber gets every message, and Broadcast returns how many subscribers it handed the message to.
//
// Each subscriber runs in its own goroutine, so the output of different subscribers interleaves.
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
	names := []string{"alice", "bob"}
	ids := make([]uuid.UUID, 0, len(names))
	for _, name := range names {
		id, err := b.Subscribe(ctx, func(_ context.Context, msg string) error {
			defer handled.Done()
			fmt.Println(name, "got", msg)

			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
		ids = append(ids, id)
	}

	for _, msg := range []string{"hello", "bye"} {
		handled.Add(len(names))
		n, err := b.Broadcast(ctx, msg)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(msg, "handed to", n, "subscribers")
	}
	handled.Wait()

	for _, id := range ids {
		if err := b.Unsubscribe(ctx, id); err != nil {
			log.Fatal(err)
		}
	}
}
