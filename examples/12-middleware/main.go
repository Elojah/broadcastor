// subscriber.WithMiddleware wraps handle, the first middleware outermost. Here middleware.Retry makes up to 3 attempts
// at a retryable error, waiting 10ms then 20ms. logAttempts, a middleware of our own, prints every failed attempt, and
// middleware.WrapError wraps the last error for the error handler.
//
// Middlewares run in the subscriber's goroutine, so the output is in message order, and Broadcast waits while Retry
// does.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
)

var (
	errUnavailable = errors.New("unavailable")
	errUnknownJob  = errors.New("unknown job")
)

// logAttempts prints every call to the handler it wraps that fails, numbered per job. Only the subscriber's goroutine
// calls it.
func logAttempts() subscriber.Middleware[string] {
	attempts := map[string]int{}

	return func(next subscriber.Handler[string]) subscriber.Handler[string] {
		return func(ctx context.Context, id uuid.UUID, job string) error {
			attempts[job]++
			err := next(ctx, id, job)
			if err != nil {
				fmt.Printf("%s: attempt %d failed: %v\n", job, attempts[job], err)
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
		left, known := failures[job]
		if !known {
			return errUnknownJob
		}
		if left > 0 {
			failures[job]--

			return errUnavailable
		}
		fmt.Println(job, "done")
		done.Done()

		return nil
	},
		subscriber.WithMiddleware(
			middleware.WrapError[string](),
			middleware.Retry[string](middleware.RetryPolicy{
				Attempts:   3,
				Delay:      10 * time.Millisecond,
				Multiplier: 2,
				// An unknown job stays unknown, however many times it is tried.
				IsRetryable: func(err error) bool { return !errors.Is(err, errUnknownJob) },
			}),
			logAttempts(),
		),
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

	for _, job := range []string{"send-email", "resize-image", "charge-card", "print-invoice"} {
		done.Add(1)
		b.Broadcast(ctx, job)
	}
	done.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
