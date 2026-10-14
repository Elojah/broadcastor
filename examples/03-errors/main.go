// The error handler gets every error about the subscriber's messages. middleware.Recover turns a panic in handle into
// a *subscriber.PanicError, and middleware.WrapError wraps handle's errors in a *subscriber.HandleError naming the
// subscriber and the message. Without an error handler, errors are discarded, and without Recover, a panic crashes the
// program.
//
// The error handler runs in the subscriber's goroutine, right after handle, so the output is in message order.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
)

var errNegative = errors.New("negative")

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()

	// Every message is either handled or reported.
	var done sync.WaitGroup
	id, err := b.Subscribe(ctx, func(_ context.Context, _ uint64, n int) error {
		switch {
		case n < 0:
			return errNegative
		case n == 0:
			panic("division by zero")
		}
		fmt.Println("100 /", n, "=", 100/n)
		done.Done()

		return nil
	},
		// Recover first, so that it catches a panic in every later middleware too.
		subscriber.WithMiddleware(middleware.Recover[int](), middleware.WrapError[int]()),
		subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
			var (
				panicErr  *subscriber.PanicError[int]
				handleErr *subscriber.HandleError[int]
			)
			switch {
			case errors.As(err, &panicErr):
				fmt.Printf("panic on %d: %v\n", panicErr.Message, panicErr.Value)
			case errors.As(err, &handleErr):
				fmt.Printf("failed on %d: %v\n", handleErr.Message, handleErr.Err)
			}
			done.Done()
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	for _, n := range []int{4, -2, 0, 5} {
		done.Add(1)
		b.Broadcast(ctx, n)
	}
	done.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
