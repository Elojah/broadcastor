// With WithSubscriberBuffer, Broadcast only waits for a busy subscriber once its buffer is full.
//
// handle waits for release, standing in for slow work.
package main

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/elojah/broadcastor"
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()

	release := make(chan struct{})
	var handled sync.WaitGroup
	id, err := b.Subscribe(ctx, func(_ context.Context, n int) error {
		<-release
		fmt.Println("got", n)
		handled.Done()

		return nil
	}, broadcastor.WithSubscriberBuffer[int](2))
	if err != nil {
		log.Fatal(err)
	}

	// The subscriber takes 1 and waits in handle, while 2 and 3 wait in its buffer. Without the buffer, the second
	// Broadcast would wait for handle, forever.
	for _, n := range []int{1, 2, 3} {
		handled.Add(1)
		b.Broadcast(ctx, n)
	}
	fmt.Println("3 Broadcasts returned")
	close(release)
	handled.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
