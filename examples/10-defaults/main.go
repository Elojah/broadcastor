// Subscribers with different needs, set up with WithSubscriberDefaultMessageOptions. The log must get every message, so
// Broadcast waits for it, as by default. The dashboard is slow and must not hold Broadcast up, so it defaults to
// WithMessageNonBlocking and misses updates while it is busy. A Broadcast that everyone must get passes
// WithMessageSync, which overrides the dashboard's default.
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
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[string]()

	// Each subscriber is done once it has handled "done", which it gets last.
	var finished sync.WaitGroup
	finished.Add(2)

	logID, err := b.Subscribe(ctx, func(_ context.Context, msg string) error {
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
	dashboardID, err := b.Subscribe(ctx, func(_ context.Context, msg string) error {
		<-release
		fmt.Println("dashboard:", msg)
		if msg == "done" {
			finished.Done()
		}

		return nil
	},
		broadcastor.WithSubscriberDefaultMessageOptions(broadcastor.WithMessageNonBlocking[string]()),
		broadcastor.WithSubscriberErrorHandler[string](func(_ context.Context, err error) {
			var dropped *broadcastor.DroppedError[string]
			if errors.As(err, &dropped) {
				fmt.Println("dashboard missed:", dropped.Message)
			}
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	b.Broadcast(ctx, "started", broadcastor.WithMessageSync[string]()) // the dashboard takes it, then waits in handle
	b.Broadcast(ctx, "50%")                                            // the dashboard is busy, so it misses it
	close(release)
	b.Broadcast(ctx, "done", broadcastor.WithMessageSync[string]()) // waits for the dashboard
	finished.Wait()

	for _, id := range []uuid.UUID{logID, dashboardID} {
		if err := b.Unsubscribe(ctx, id); err != nil {
			log.Fatal(err)
		}
	}
}
