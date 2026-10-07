// middleware.History puts every message the subscriber handles in a store, and subscriber.WithDeadLetters every one it
// loses: sharing one, they make an audit log of every payment, with the error if it failed.
//
// Both put from the subscriber's goroutine here, so the log is in message order. A loss in Broadcast, to a timeout or a
// full buffer, would be put from Broadcast's goroutine.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

var errInsufficientFunds = errors.New("insufficient funds")

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()

	audit := store.NewRing[int](16)
	// Only the subscriber's goroutine uses it.
	balance := 100
	id, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, amount int) error {
		if amount > balance {
			return errInsufficientFunds
		}
		balance -= amount

		return nil
	},
		subscriber.WithMiddleware(middleware.History[int](audit)),
		subscriber.WithDeadLetters[int](audit),
	)
	if err != nil {
		log.Fatal(err)
	}

	payments := []int{30, 80, 50, 40}
	for _, amount := range payments {
		b.Broadcast(ctx, amount)
	}

	// Next waits for each payment to be in the log.
	for range payments {
		entry, err := audit.Next(ctx)
		if err != nil {
			log.Fatal(err)
		}
		outcome := "paid"
		if entry.Err != nil {
			outcome = entry.Err.Error()
		}
		fmt.Printf("%d: %s\n", entry.Message, outcome)
		if err := audit.Ack(ctx, entry.ID); err != nil {
			log.Fatal(err)
		}
	}

	if err := b.Unsubscribe(ctx, id); err != nil {
		log.Fatal(err)
	}
}
