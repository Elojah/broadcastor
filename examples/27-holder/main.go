// subscriber.WithHolder holds each message the subscriber takes until its holder releases it to handle. Two sensors
// send readings in bursts. The display shows only the latest reading of each burst, whichever the sensor
// (holder.Latest), and the uplink sends the latest of each sensor, with how many readings it stands for
// (holder.Reduce). Each window ends half a second after its first reading.
//
// Shutdown releases what is still held at once: no reading can come any more, so none misses its window.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/holder"
	"github.com/elojah/broadcastor/subscriber"
)

// window is far longer than a burst takes to come.
const window = 500 * time.Millisecond

type reading struct {
	Sensor  string
	Celsius float64
	// Count is how many readings this one stands for.
	Count int
}

func read(sensor string, celsius float64) reading {
	return reading{Sensor: sensor, Celsius: celsius, Count: 1}
}

// latestBySensor replaces the reading of next's sensor with next, counting both, or appends next.
func latestBySensor(held []reading, next reading) []reading {
	for i, r := range held {
		if r.Sensor == next.Sensor {
			next.Count += r.Count
			held[i] = next

			return held
		}
	}

	return append(held, next)
}

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[reading]()
	// Each handle sends on it once it has printed, since the two subscribers run concurrently.
	shown := make(chan struct{}, 16)

	subscribe(ctx, b, holder.NewLatest[reading](window), func(_ context.Context, _ uuid.UUID, r reading) error {
		fmt.Printf("display: %s at %.1f°C\n", r.Sensor, r.Celsius)
		shown <- struct{}{}

		return nil
	})
	subscribe(ctx, b, holder.NewReduce(window, latestBySensor), func(_ context.Context, _ uuid.UUID, r reading) error {
		fmt.Printf("uplink: %s at %.1f°C, latest of %d\n", r.Sensor, r.Celsius, r.Count)
		shown <- struct{}{}

		return nil
	})

	for _, r := range []reading{read("north", 20.1), read("south", 18.0), read("north", 20.3), read("north", 20.2), read("south", 18.4)} {
		b.Broadcast(ctx, r)
	}
	// The windows end: one reading on the display, one per sensor on the uplink.
	for range 3 {
		<-shown
	}

	for _, r := range []reading{read("north", 21.0), read("north", 21.5)} {
		b.Broadcast(ctx, r)
	}
	if err := b.Shutdown(ctx); err != nil {
		log.Fatal(err)
	}
}

func subscribe(ctx context.Context, b *broadcastor.Broadcastor[reading], h subscriber.Holder[reading], handle subscriber.Handler[reading]) {
	if _, err := b.Subscribe(ctx, handle, subscriber.WithHolder(h)); err != nil {
		log.Fatal(err)
	}
}
