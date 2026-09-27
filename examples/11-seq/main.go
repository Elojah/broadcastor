// SubscribeSeq hands a subscriber's messages to a range loop instead of a handle function. The loop body runs in the
// caller's goroutine, one message at a time, and breaking out of the loop unsubscribes. Cancelling the ctx given to
// SubscribeSeq would end the loop too.
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

	_, msgs, err := b.SubscribeSeq(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// The loop runs in main's goroutine, so the messages are broadcast from another one.
	broadcasted := make(chan struct{})
	go func() {
		defer close(broadcasted)
		for _, msg := range []string{"hello", "world", "stop", "never seen"} {
			if _, err := b.Broadcast(ctx, msg); err != nil {
				log.Fatal(err)
			}
		}
	}()

	for msg := range msgs {
		fmt.Println("got", msg)
		if msg == "stop" {
			break
		}
	}
	<-broadcasted

	n, err := b.Broadcast(ctx, "anyone?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("handed to", n, "subscribers")
}
