// subscriber.WithOrder makes a subscriber handle its messages in their own order, here the time each reading was taken,
// rather than in the order they come. The sensors' links differ, so their readings arrive out of order. While handle
// is busy with the first, the others queue in the buffer, and the subscriber handles them by time. East's reading was
// taken before the one already handled: it is late, and reported as a *subscriber.LateError.
//
// With an OrderPolicy.Window, the subscriber would also wait that long for readings still on their way.
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
	"github.com/elojah/broadcastor/subscriber"
)

type reading struct {
	Sensor string
	At     time.Time
}

func (r reading) String() string {
	return r.Sensor + " at " + r.At.Format(time.TimeOnly)
}

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[reading]()

	at := func(seconds int) time.Time {
		return time.Date(2026, 10, 1, 10, 0, seconds, 0, time.UTC)
	}
	readings := []reading{{"north", at(2)}, {"south", at(3)}, {"east", at(1)}, {"north", at(5)}, {"west", at(4)}}

	// Done once for each reading, handled or late.
	var done sync.WaitGroup
	done.Add(len(readings))
	busy, release := make(chan struct{}), make(chan struct{})
	first := true
	_, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, r reading) error {
		defer done.Done()
		fmt.Println("handled", r)
		if first {
			first = false
			close(busy)
			<-release
		}

		return nil
	},
		subscriber.WithBuffer[reading](len(readings)),
		subscriber.WithOrder(subscriber.OrderPolicy[reading]{Compare: subscriber.ByTime(func(r reading) time.Time { return r.At })}),
		subscriber.WithErrorHandler[reading](func(_ context.Context, err error) {
			var late *subscriber.LateError[reading]
			if errors.As(err, &late) {
				fmt.Println("late", late.Message, "after", late.After)
				done.Done()
			}
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	b.Broadcast(ctx, readings[0])
	<-busy // handle is busy with it, so the others queue
	for _, r := range readings[1:] {
		b.Broadcast(ctx, r)
	}
	close(release)
	done.Wait()

	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
}
