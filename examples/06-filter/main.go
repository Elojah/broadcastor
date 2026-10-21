// subscriber.WithFilter skips messages before Publish sends them, so the subscriber never wakes up for them. A
// thermometer publishes a reading every second, mostly the same. The display shows one only once it has moved by half
// a degree from the last shown (filter.Changed, a deadband), and the uplink sends at most one an hour (filter.Every),
// so only the first here.
//
// Publish returns how many subscribers took each reading, skipped ones excluded.
package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/elojah/multicast"
	"github.com/elojah/multicast/filter"
	"github.com/elojah/multicast/subscriber"
)

func main() {
	ctx := context.Background()
	g := multicast.New[float64]()
	// Each handle sends what it got, since the two subscribers run concurrently.
	got := make(chan string, 16)

	_, err := g.Subscribe(ctx, func(_ context.Context, _ uint64, celsius float64) error {
		got <- fmt.Sprintf("display: %.1f degrees", celsius)

		return nil
	}, subscriber.WithFilter(filter.Changed(func(prev, next float64) bool { return math.Abs(next-prev) >= 0.5 })))
	if err != nil {
		log.Fatal(err)
	}

	_, err = g.Subscribe(ctx, func(_ context.Context, _ uint64, celsius float64) error {
		got <- fmt.Sprintf("uplink: %.1f degrees", celsius)

		return nil
	}, subscriber.WithFilter(filter.Every[float64](time.Hour)))
	if err != nil {
		log.Fatal(err)
	}

	readings := []float64{20.0, 20.1, 20.2, 20.6, 20.4, 19.9, 20.0}
	taken := 0
	for _, celsius := range readings {
		taken += g.Publish(ctx, celsius)
	}
	for range taken {
		fmt.Println(<-got)
	}
	fmt.Println(len(readings), "readings, handed over", taken, "times")

	if err := g.Close(); err != nil {
		log.Fatal(err)
	}
}
