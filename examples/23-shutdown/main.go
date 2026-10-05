// On SIGTERM, or a power-fail signal, a device has a few seconds left. Close never waits, so a program that exits right
// after it cuts off whatever handle was doing. Shutdown is Close, then waits until every subscriber has handled what it
// took, or until its ctx is done. Here a logger is writing a reading to flash, with two more in its buffer, and an
// uplink that is down puts every reading it misses in a dead-letter queue, to send after the reboot. Once Shutdown
// returns, every reading is logged, and every one the uplink missed is queued.
//
// A program would wait for the signal with signal.NotifyContext. Here, it comes once the logger is busy.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

var errUplinkDown = errors.New("uplink down")

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()

	busy, release := make(chan struct{}), make(chan struct{})
	_, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, n int) error {
		if n == 1 {
			close(busy)
			<-release // a slow write to flash
		}
		fmt.Println("logged", n)

		return nil
	}, subscriber.WithBuffer[int](2))
	if err != nil {
		log.Fatal(err)
	}

	queue := store.NewRing[int](16)
	_, err = b.Subscribe(ctx, func(context.Context, uuid.UUID, int) error {
		<-release // until the uplink times out

		return errUplinkDown
	},
		subscriber.WithBuffer[int](2),
		subscriber.WithDeadLetters[int](queue),
		// What is left in the buffer once shut down is queued at once, rather than after a timeout each.
		subscriber.WithUnsubscribeOptions[int](subscriber.WithUnsubscribeDiscard()),
	)
	if err != nil {
		log.Fatal(err)
	}

	for n := 1; n <= 3; n++ {
		b.Broadcast(ctx, n)
	}
	<-busy

	// SIGTERM. Without Shutdown waiting, the program could exit before logging 2 and 3, or queuing them.
	close(release)
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := b.Shutdown(shutdownCtx); err != nil {
		log.Fatal(err)
	}

	for queue.Len() > 0 {
		entry, err := queue.Next(ctx)
		if err != nil {
			log.Fatal(err)
		}
		// Failed to send, or discarded if the signal came first, which depends on scheduling.
		fmt.Println("queued", entry.Message)
		if err := queue.Ack(ctx, entry.ID); err != nil {
			log.Fatal(err)
		}
	}
}
