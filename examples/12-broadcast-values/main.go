// WithSubscriberBroadcastValues makes the values of the Broadcast ctx, such as a request's trace ID, reach handle, on
// top of those of the ctx passed to Subscribe. handle still gets the cancellation of the Subscribe ctx alone, so it is
// not cancelled when the request that broadcast the message is over, which may well be before handle runs.
package main

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/elojah/broadcastor"
)

type (
	componentKey struct{}
	traceIDKey   struct{}
)

func main() {
	ctx := context.WithValue(context.Background(), componentKey{}, "mailer")
	b := broadcastor.NewBroadcastor[string]()

	var handled sync.WaitGroup
	id, err := b.Subscribe(ctx, func(ctx context.Context, msg string) error {
		defer handled.Done()
		fmt.Printf("%v [%v]: %s (ctx err: %v)\n", ctx.Value(componentKey{}), ctx.Value(traceIDKey{}), msg, ctx.Err())

		return nil
	}, broadcastor.WithSubscriberBroadcastValues[string]())
	if err != nil {
		log.Fatal(err)
	}

	// Each request broadcasts with a ctx of its own, cancelled once the request is over.
	for _, trace := range []string{"trace-1", "trace-2"} {
		requestCtx, cancel := context.WithCancel(context.WithValue(context.Background(), traceIDKey{}, trace))
		handled.Add(1)
		b.Broadcast(requestCtx, "order placed")
		cancel()
	}
	handled.Wait()

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
