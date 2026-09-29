// Subscribers with different defaults (subscriber.WithDefaultMessageOptions): Broadcast waits for the log, while the
// slow dashboard defaults to message.WithNonBlocking and misses updates while busy. message.WithSync overrides that for
// a message everyone must get.
//
// The dashboard's handle waits for release, standing in for slow work.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	// Each subscriber is done once it has handled "done", which it gets last.
	var finished sync.WaitGroup
	finished.Add(2)

	logID, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, msg string) error {
		fmt.Println("log:", msg)
		if msg == "done" {
			finished.Done()
		}

		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	release := make(chan struct{})
	dashboardID, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, msg string) error {
		<-release
		fmt.Println("dashboard:", msg)
		if msg == "done" {
			finished.Done()
		}

		return nil
	},
		subscriber.WithDefaultMessageOptions(message.WithNonBlocking[string]()),
		subscriber.WithErrorHandler[string](func(_ context.Context, err error) {
			var dropped *subscriber.DroppedError[string]
			if errors.As(err, &dropped) {
				fmt.Println("dashboard missed:", dropped.Message)
			}
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	b.Broadcast(ctx, "started", message.WithSync[string]()) // the dashboard takes it, then waits in handle
	b.Broadcast(ctx, "50%")                                 // the dashboard is busy, so it misses it
	close(release)
	b.Broadcast(ctx, "done", message.WithSync[string]()) // waits for the dashboard
	finished.Wait()

	for _, id := range []uuid.UUID{logID, dashboardID} {
		if err := b.Unsubscribe(ctx, id); err != nil {
			log.Fatal(err)
		}
	}
}
