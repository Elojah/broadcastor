// SubscribeSeq returns the messages as an iterator, whose for range loop replaces handle. fail takes the error handle
// would return, here for the error handler. Breaking out unsubscribes, and so does cancelling the SubscribeSeq ctx.
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
