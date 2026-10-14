// store.File keeps on disk the readings the alerts fail to page (subscriber.WithDeadLetters), for
// subscriber.WithReplay to hand back after a restart: 19-redis without a server. The pager is down before the restart,
// and back after it.
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

var (
	errPagerDown = errors.New("pager unreachable")
	errReading   = errors.New("malformed reading")
)

type reading struct {
	Sensor  string
	At      time.Time
	Celsius int64
}

func (r reading) String() string {
	return fmt.Sprintf("%s at %s, %d°C", r.Sensor, r.At.Format(time.TimeOnly), r.Celsius)
}

func at(seconds int, celsius int64) reading {
	return reading{"north", time.Date(2026, 10, 1, 10, 0, seconds, 0, time.UTC), celsius}
}

// encode and decode turn a reading into bytes and back: store.File imports no codec.
func encode(r reading) ([]byte, error) {
	b := binary.AppendVarint(nil, r.At.Unix())
	b = binary.AppendVarint(b, r.Celsius)

	return append(b, r.Sensor...), nil
}

func decode(b []byte) (reading, error) {
	unix, n := binary.Varint(b)
	if n <= 0 {
		return reading{}, errReading
	}
	celsius, m := binary.Varint(b[n:])
	if m <= 0 {
		return reading{}, errReading
	}

	return reading{Sensor: string(b[n+m:]), At: time.Unix(unix, 0).UTC(), Celsius: celsius}, nil
}

func main() {
	dir, err := os.MkdirTemp("", "broadcastor-file")
	if err != nil {
		log.Fatal(err)
	}
	// The same process twice: the pager is down before the restart, and back after it.
	runAlerts(dir, true, at(0, 20), at(1, 31), at(2, 22), at(3, 33))
	runAlerts(dir, false, at(4, 34))
	if err := os.RemoveAll(dir); err != nil {
		log.Fatal(err)
	}
}

// runAlerts subscribes the alerts, which first page what they lost, then broadcasts readings, and shuts down once each
// is handled or lost. Paging fails while pagerDown.
func runAlerts(dir string, pagerDown bool, readings ...reading) {
	ctx := context.Background()
	// Synced a second after a write rather than on each one, since flash wears with each sync: a power cut loses at
	// most the last second's losses. Close syncs the rest.
	lost, err := store.OpenFile(dir, encode, decode, store.WithSyncInterval(time.Second))
	if err != nil {
		log.Fatal(err)
	}

	b := broadcastor.NewBroadcastor[reading]()
	_, err = b.Subscribe(ctx, func(_ context.Context, _ uint64, r reading) error {
		if r.Celsius <= 30 {
			return nil
		}
		if pagerDown {
			fmt.Println("alerts failed to page", r)

			return errPagerDown
		}
		fmt.Println("alerts paged", r)

		return nil
	},
		// Before any reading broadcast here. Each is acked once handled, or lost again and back in lost.
		subscriber.WithReplay(lost.Replay(ctx)),
		subscriber.WithDeadLetters[reading](lost),
		// handle prints the others.
		subscriber.WithErrorHandler[reading](func(_ context.Context, err error) {
			if errors.Is(err, subscriber.ErrStore) {
				log.Println(err)
			}
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	for _, r := range readings {
		b.Broadcast(ctx, r)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := b.Shutdown(shutdownCtx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("lost and not paged yet:", lost.Len())
	if err := lost.Close(); err != nil {
		log.Fatal(err)
	}
}
