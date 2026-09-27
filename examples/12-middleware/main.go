// subscriber.WithMiddleware wraps handle in middlewares: each one is called with the message, and calls the next one,
// or not. Here retry calls handle again when it fails, up to 3 attempts. Only the error of the last attempt reaches
// middleware.WrapError, given first so that it wraps retry, which makes it a *subscriber.HandleError for the error
// handler.
//
// Middlewares run in the subscriber's goroutine, like handle and the error handler, so the output is in message order.
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

var errUnavailable = errors.New("unavailable")

// retry calls next up to attempts times, until it succeeds, and returns the error of the last attempt.
func retry(attempts int) subscriber.Middleware[string] {
	return func(next subscriber.Handler[string]) subscriber.Handler[string] {
		return func(ctx context.Context, id uuid.UUID, job string) error {
			var err error
			for attempt := 1; attempt <= attempts; attempt++ {
				if err = next(ctx, id, job); err == nil {
					return nil
				}
				fmt.Printf("%s: attempt %d failed: %v\n", job, attempt, err)
			}

			return err
		}
	}
}

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	// How many more times each job fails before it succeeds. Only the subscriber's goroutine uses it.
	failures := map[string]int{"send-email": 1, "resize-image": 0, "charge-card": 5}

	// Every message is either handled or reported.
	var done sync.WaitGroup
	id, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, job string) error {
		if failures[job] > 0 {
			failures[job]--

			return errUnavailable
		}
		fmt.Println(job, "done")
		done.Done()

		return nil
	},
		subscriber.WithMiddleware(middleware.WrapError[string](), retry(3)),
		subscriber.WithErrorHandler[string](func(_ context.Context, err error) {
			var handleErr *subscriber.HandleError[string]
			if errors.As(err, &handleErr) {
				fmt.Printf("gave up on %s: %v\n", handleErr.Message, handleErr.Err)
			}
			done.Done()
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	for _, job := range []string{"send-email", "resize-image", "charge-card"} {
		done.Add(1)
		b.Broadcast(ctx, job)
	}
	done.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
