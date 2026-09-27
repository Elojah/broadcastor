// With middleware.Recover, a panic in handle is reported to the error handler as a *subscriber.PanicError, and the
// subscriber goes on with the next message. Without it, the panic crashes the program.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()

	// Every message is either handled or reported.
	var done sync.WaitGroup
	id, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, n int) error {
		fmt.Println("100 /", n, "=", 100/n)
		done.Done()

		return nil
	},
		subscriber.WithMiddleware(middleware.Recover[int]()),
		subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
			var panicErr *subscriber.PanicError[int]
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
		b.Broadcast(ctx, n)
	}
	done.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
