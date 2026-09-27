// A non-blocking Broadcast never waits: a subscriber that is busy in handle, or whose buffer is full, misses the
// message, and its error handlers are given a *DroppedError.
//
// handle waits for release, standing in for slow work.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/elojah/broadcastor"
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	release := make(chan struct{})
	var handled sync.WaitGroup
	id, err := b.Subscribe(ctx, func(_ context.Context, msg string) error {
		<-release
		fmt.Println("got", msg)
		handled.Done()

		return nil
	}, broadcastor.WithSubscriberErrorHandler[string](func(_ context.Context, err error) {
		var dropped *broadcastor.DroppedError[string]
		if errors.As(err, &dropped) {
			fmt.Println("dropped", dropped.Message)
		}
	}))
	if err != nil {
		log.Fatal(err)
	}

	handled.Add(1)
	// The subscriber takes it, then waits in handle.
	if _, err := b.Broadcast(ctx, "first"); err != nil {
		log.Fatal(err)
	}
	n, err := b.Broadcast(ctx, "second", broadcastor.WithMessageNonBlocking[string]())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("second handed to", n, "subscribers")
	close(release)
	handled.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
