// SubscribeSeq returns the subscriber's messages as an iterator, so that a for range loop takes the place of handle.
// Breaking out of the loop unsubscribes, and so does cancelling the ctx passed to SubscribeSeq.
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

	_, seq, err := b.SubscribeSeq(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Each Broadcast waits for the loop to take its message, as it would for handle.
	go func() {
		for _, msg := range []string{"hello", "world", "stop"} {
			b.Broadcast(ctx, msg)
		}
	}()

	for msg := range seq {
		fmt.Println("got", msg)
		if msg == "stop" {
			break
		}
	}

	fmt.Println("handed to", b.Broadcast(ctx, "anyone?"), "subscribers")
}
