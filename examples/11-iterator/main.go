// SubscribeSeq returns the subscriber's messages as an iterator, so that a for range loop takes the place of handle.
// Each message comes with fail, which takes the error handle would return: here the error handler gets it. Breaking
// out of the loop unsubscribes, and so does cancelling the ctx passed to SubscribeSeq.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/subscriber"
)

var errEmpty = errors.New("empty message")

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	_, seq, err := b.SubscribeSeq(ctx, subscriber.WithErrorHandler[string](func(_ context.Context, err error) {
		fmt.Println("failed:", err)
	}))
	if err != nil {
		log.Fatal(err)
	}

	// Each Broadcast waits for the loop to take its message, as it would for handle.
	go func() {
		for _, msg := range []string{"hello", "", "world", "stop"} {
			b.Broadcast(ctx, msg)
		}
	}()

	for msg, fail := range seq {
		if msg == "" {
			fail(errEmpty)

			continue
		}
		fmt.Println("got", msg)
		if msg == "stop" {
			break
		}
	}

	fmt.Println("handed to", b.Broadcast(ctx, "anyone?"), "subscribers")
}
