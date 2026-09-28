// message.WithContext gives a message a ctx of its own, which handle, its middlewares and the error handlers get instead
// of the ctx passed to Subscribe. The ctx passed to Broadcast only bounds how long Broadcast waits, and its values never
// reach handle.
//
// Here a request broadcasts asynchronously and returns while the subscriber is still busy. Its ctx, cancelled once it
// returns, would make the pending send give up, so the Broadcast is detached from it with context.WithoutCancel and
// bounded by a timeout instead. The same detached ctx carries the request ID to handle and to the error handler.
//
// handle waits for release, standing in for slow work.
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
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

var errUnavailable = errors.New("unavailable")

type requestIDKey struct{}

func requestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}

	return "none"
}

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	release := make(chan struct{})
	var handled sync.WaitGroup
	id, err := b.Subscribe(ctx, func(ctx context.Context, _ uuid.UUID, msg string) error {
		<-release
		fmt.Printf("got %s (request %s, ctx done: %t)\n", msg, requestID(ctx), ctx.Err() != nil)
		if msg == "hello" {
			return errUnavailable
		}
		handled.Done()

		return nil
	}, subscriber.WithErrorHandler[string](func(ctx context.Context, err error) {
		fmt.Printf("failed: %v (request %s)\n", err, requestID(ctx))
		handled.Done()
	}))
	if err != nil {
		log.Fatal(err)
	}

	handled.Add(2)
	b.Broadcast(ctx, "first") // the subscriber takes it, then waits in handle

	request := func() {
		reqCtx, cancel := context.WithCancel(context.WithValue(ctx, requestIDKey{}, "req-42"))
		defer cancel() // the request is over, but the subscriber has not taken "hello" yet

		detached := context.WithoutCancel(reqCtx)
		n := b.Broadcast(detached, "hello",
			message.WithAsync[string](),
			message.WithTimeout[string](time.Second),
			message.WithContext[string](detached),
		)
		fmt.Println("hello handed to", n, "subscribers")
	}
	request()
	fmt.Println("request returned")
	close(release)
	handled.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
