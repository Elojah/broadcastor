// Broadcastor.BroadcastBatch hands several messages at once, with one Config. A poll reads every sensor at once, and
// broadcasts the readings as one batch, while a handheld probe sends its readings one at a time. The display has no
// batch handler, so it gets each reading of a batch alone. The uplink, billed per request, gathers whatever it takes
// into batches of three (holder.NewBatch), and sends each batch as one request from its batch handler
// (subscriber.WithBatchHandler).
//
// Shutdown hands the uplink's last, partial batch to its batch handler at once, without waiting for its window to end.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/holder"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

type reading struct {
	Sensor  string
	Celsius float64
}

func (r reading) String() string {
	return fmt.Sprintf("%s %.1f°C", r.Sensor, r.Celsius)
}

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[reading]()

	if _, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, r reading) error {
		fmt.Println("display:", r)

		return nil
	}); err != nil {
		log.Fatal(err)
	}

	// Each batch goes to send, so handle gets nothing.
	send := func(_ context.Context, _ uuid.UUID, batch message.Batch[reading]) error {
		readings := make([]string, 0, batch.Len())
		for r := range batch.All() {
			readings = append(readings, r.String())
		}
		fmt.Println("uplink: one request for", strings.Join(readings, ", "))

		return nil
	}
	if _, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, r reading) error {
		fmt.Println("uplink: alone", r)

		return nil
	}, subscriber.WithHolder(holder.NewBatch[reading](time.Hour, 3)), subscriber.WithBatchHandler(send)); err != nil {
		log.Fatal(err)
	}

	b.BroadcastBatch(ctx, &message.Slice[reading]{{"north", 20.1}, {"south", 18.0}, {"east", 19.5}})
	for _, celsius := range []float64{17.2, 17.4, 17.9, 18.1} {
		b.Broadcast(ctx, reading{"probe", celsius})
	}

	if err := b.Shutdown(ctx); err != nil {
		log.Fatal(err)
	}
}
