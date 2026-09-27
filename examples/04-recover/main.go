// With WithSubscriberRecover, a panic in handle is reported to the error handler as a *PanicError, and the subscriber
// goes on with the next message. Without it, the panic crashes the program.
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
	b := broadcastor.NewBroadcastor[int]()

	// Every message is either handled or reported.
	var done sync.WaitGroup
	id, err := b.Subscribe(ctx, func(_ context.Context, n int) error {
		fmt.Println("100 /", n, "=", 100/n)
		done.Done()

		return nil
	},
		broadcastor.WithSubscriberRecover[int](),
		broadcastor.WithSubscriberErrorHandler[int](func(_ context.Context, err error) {
			var panicErr *broadcastor.PanicError[int]
			if errors.As(err, &panicErr) {
				fmt.Printf("panic on %d: %v\n", panicErr.Message, panicErr.Value)
			}
			done.Done()
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	for _, n := range []int{4, 0, 5} {
		done.Add(1)
		if _, err := b.Broadcast(ctx, n); err != nil {
			log.Fatal(err)
		}
	}
	done.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
