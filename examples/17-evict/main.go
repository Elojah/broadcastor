// subscriber.WithEvictAfter unsubscribes a subscriber that loses too many messages in a row. A stuck subscriber loses 2
// and 3 by timeout, and 3 evicts it: the error handlers, then the callback, get a *subscriber.EvictedError wrapping the
// *subscriber.TimeoutError, and Broadcast no longer waits for it.
//
// handle waits for release, standing in for slow work.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()

	release := make(chan struct{})
	_, err := b.Subscribe(ctx, func(context.Context, uuid.UUID, int) error {
		<-release

		return nil
	}, subscriber.WithEvictAfter(2, func(_ context.Context, evicted *subscriber.EvictedError[int]) {
		fmt.Println("stuck evicted on losing", evicted.Message)
	}), subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
		var timeout *subscriber.TimeoutError[int]
		if errors.As(err, &timeout) {
			fmt.Printf("stuck lost %d, evicted: %t\n", timeout.Message, errors.Is(err, subscriber.ErrEvicted))
		}
	}))
	if err != nil {
		log.Fatal(err)
	}

	// Its buffer has room for every message, so Broadcast never waits for it.
	_, err = b.Subscribe(ctx, func(context.Context, uuid.UUID, int) error {
		return nil
	}, subscriber.WithBuffer[int](4))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("1 handed to", b.Broadcast(ctx, 1), "subscribers") // stuck takes it, then waits in handle
	for n := 2; n <= 4; n++ {
		fmt.Println(n, "handed to", b.Broadcast(ctx, n, message.WithTimeout[int](10*time.Millisecond)), "subscribers")
	}
	fmt.Println("subscribers left:", len(b.Stats()))

	close(release)
	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
}
