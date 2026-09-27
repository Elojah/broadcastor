// The error handler gets every error handle returns. middleware.WrapError wraps each one in a *subscriber.HandleError,
// along with the subscriber and the message it failed on. Without an error handler, errors are discarded.
//
// The error handler runs in the subscriber's goroutine, right after handle, so the output is in message order.
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

var errNegative = errors.New("negative")

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()

	// Every message is either handled or reported.
	var done sync.WaitGroup
	id, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, n int) error {
		if n < 0 {
			return errNegative
		}
		fmt.Println("got", n)
		done.Done()

		return nil
	},
		subscriber.WithMiddleware(middleware.WrapError[int]()),
		subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
			var handleErr *subscriber.HandleError[int]
			if errors.As(err, &handleErr) {
				fmt.Printf("failed on %d: %v\n", handleErr.Message, handleErr.Err)
			}
			done.Done()
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	for _, n := range []int{1, -2, 3, -4} {
		done.Add(1)
		b.Broadcast(ctx, n)
	}
	done.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
