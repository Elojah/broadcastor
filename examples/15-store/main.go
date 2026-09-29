// A store is given every message a subscriber loses. Here handle fails on negative numbers, and Close makes the
// subscriber discard the two messages left in its buffer (subscriber.WithUnsubscribeDiscard), so none is lost.
//
// The store is called from the subscriber's goroutine, so the output is in message order.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/subscriber"
)

var errNegative = errors.New("negative")

// deadLetters is a store that keeps every message it is given, in memory.
type deadLetters struct {
	done *sync.WaitGroup

	mu      sync.Mutex
	records []subscriber.Record[int]
}

func (d *deadLetters) Put(_ context.Context, r subscriber.Record[int]) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, r)
	d.done.Done()

	return nil
}

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()

	// Every message is either handled or stored.
	var done sync.WaitGroup
	lost := &deadLetters{done: &done}
	release := make(chan struct{})
	_, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, n int) error {
		if n < 0 {
			return errNegative
		}
		fmt.Println("got", n)
		done.Done()
		if n == 3 {
			<-release // busy until Close, so that 4 and 5 are still in the buffer
		}

		return nil
	},
		subscriber.WithBuffer[int](2),
		subscriber.WithStore[int](lost),
		subscriber.WithUnsubscribeOptions[int](subscriber.WithUnsubscribeDiscard()),
	)
	if err != nil {
		log.Fatal(err)
	}

	for _, n := range []int{1, -2, 3, 4, 5} {
		done.Add(1)
		b.Broadcast(ctx, n)
	}
	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
	close(release)
	done.Wait()

	lost.mu.Lock()
	defer lost.mu.Unlock()
	for _, r := range lost.records {
		reason := r.Err.Error()
		if errors.Is(r.Err, subscriber.ErrClosed) {
			reason = "unsubscribed"
		}
		fmt.Printf("stored %d: %s\n", r.Message, reason)
	}
}
