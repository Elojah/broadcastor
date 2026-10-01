// broadcastor.WithHistory gives every message broadcast to a subscriber.History, here a store.History that keeps the
// last ones in memory, and subscriber.WithReplay hands them to a subscriber that joins late, before the live ones: each
// once, with none missed in between. Here a dashboard starts once readings have already come in, and replays those
// taken from 10:00:02 on.
package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/store"
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
	b := broadcastor.NewBroadcastor(broadcastor.WithHistory(store.NewHistory[reading](10)))

	at := func(seconds int) time.Time {
		return time.Date(2026, 10, 1, 10, 0, seconds, 0, time.UTC)
	}
	for s := range 4 {
		r := reading{"north", at(s)}
		fmt.Println(r, "handed to", b.Broadcast(ctx, r), "subscribers")
	}

	since := at(2)
	var handled sync.WaitGroup
	handled.Add(4) // two replayed, two live
	_, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, r reading) error {
		fmt.Println("dashboard got", r)
		handled.Done()

		return nil
	}, subscriber.WithReplay(func(r reading) bool { return !r.At.Before(since) }))
	if err != nil {
		log.Fatal(err)
	}

	for s := 4; s < 6; s++ {
		b.Broadcast(ctx, reading{"north", at(s)})
	}
	handled.Wait()

	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
}
