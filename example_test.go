package broadcastor_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
)

var errNegative = errors.New("negative")

// Every subscriber gets every message, in the order it was broadcast. Each one runs in its own goroutine, so the output
// of different subscribers interleaves.
func Example() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	names := []string{"alice", "bob"}
	ids := make([]uuid.UUID, 0, len(names))
	var done sync.WaitGroup
	for _, name := range names {
		done.Add(1)
		id, err := b.Subscribe(ctx, func(_ context.Context, msg string) error {
			fmt.Println(name, "got", msg)
			if msg == "bye" {
				done.Done()
			}

			return nil
		})
		if err != nil {
			fmt.Println(err)

			return
		}
		ids = append(ids, id)
	}

	b.Broadcast(ctx, "hello")
	b.Broadcast(ctx, "bye")
	done.Wait()

	for _, id := range ids {
		if err := b.Unsubscribe(ctx, id); err != nil {
			fmt.Println(err)
		}
	}

	// Unordered output:
	// alice got hello
	// bob got hello
	// alice got bye
	// bob got bye
}

// The error handler is given every error handle returns, wrapped in a *HandleError along with the message it failed on.
func ExampleWithSubscriberErrorHandler() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()

	failures := make(chan *broadcastor.HandleError[int], 2)
	id, err := b.Subscribe(ctx, func(_ context.Context, n int) error {
		if n < 0 {
			return errNegative
		}

		return nil
	}, broadcastor.WithSubscriberErrorHandler[int](func(_ context.Context, err error) {
		var handleErr *broadcastor.HandleError[int]
		if errors.As(err, &handleErr) {
			failures <- handleErr
		}
	}))
	if err != nil {
		fmt.Println(err)

		return
	}

	for _, n := range []int{1, -2, 3, -4} {
		b.Broadcast(ctx, n)
	}
	for range 2 {
		failure := <-failures
		fmt.Printf("message %d: %v\n", failure.Message, failure.Err)
	}

	if err := b.Unsubscribe(ctx, id); err != nil {
		fmt.Println(err)
	}

	// Output:
	// message -2: negative
	// message -4: negative
}

// An async Broadcast returns without waiting for a subscriber that is still busy with the previous message.
func ExampleWithMessageAsync() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	release, done := make(chan struct{}), make(chan struct{})
	id, err := b.Subscribe(ctx, func(_ context.Context, msg string) error {
		<-release
		fmt.Println("got", msg)
		if msg == "second" {
			close(done)
		}

		return nil
	})
	if err != nil {
		fmt.Println(err)

		return
	}

	// The subscriber takes the first message, then waits in handle. Without WithMessageAsync, the second Broadcast would
	// wait for it.
	b.Broadcast(ctx, "first")
	b.Broadcast(ctx, "second", broadcastor.WithMessageAsync[string]())
	fmt.Println("both Broadcasts returned")
	close(release)
	<-done

	if err := b.Unsubscribe(ctx, id); err != nil {
		fmt.Println(err)
	}

	// Output:
	// both Broadcasts returned
	// got first
	// got second
}

// A Broadcast with a timeout gives up on a subscriber that does not take the message in time, and tells the error
// handlers.
func ExampleWithMessageTimeout() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	release := make(chan struct{})
	id, err := b.Subscribe(ctx, func(context.Context, string) error {
		<-release

		return nil
	})
	if err != nil {
		fmt.Println(err)

		return
	}

	b.Broadcast(ctx, "first") // the subscriber takes it, then waits in handle
	n := b.Broadcast(ctx, "second", broadcastor.WithMessageTimeout[string](10*time.Millisecond),
		broadcastor.WithMessageErrorHandler[string](func(_ context.Context, err error) {
			fmt.Println("timeout:", errors.Is(err, broadcastor.ErrTimeout), errors.Is(err, context.DeadlineExceeded))
		}))
	fmt.Println("handed to", n, "subscribers")
	close(release)

	if err := b.Unsubscribe(ctx, id); err != nil {
		fmt.Println(err)
	}

	// Output:
	// timeout: true true
	// handed to 0 subscribers
}

// A subscriber can unsubscribe itself from handle, since Unsubscribe never waits.
func ExampleBroadcastor_Unsubscribe() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	done := make(chan struct{})
	var id uuid.UUID
	id, err := b.Subscribe(ctx, func(ctx context.Context, msg string) error {
		fmt.Println("got", msg)
		if msg != "stop" {
			return nil
		}
		defer close(done)

		return b.Unsubscribe(ctx, id)
	})
	if err != nil {
		fmt.Println(err)

		return
	}

	b.Broadcast(ctx, "hello")
	b.Broadcast(ctx, "stop")
	<-done
	fmt.Println("handed to", b.Broadcast(ctx, "anyone?"), "subscribers")

	// Output:
	// got hello
	// got stop
	// handed to 0 subscribers
}
