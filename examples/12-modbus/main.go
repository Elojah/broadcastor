// A gateway polls a PLC's temperature over Modbus TCP and fans it out to a fan rule and a trend, each filtering the
// repeated readings with filter.Changed. transport.Poll broadcasts the readings, and the rule writes the fan's coil over
// its own connection. middleware.Retry, with transport.IsRetryable, retries a busy PLC, then one that stopped answering:
// the transport drops the connection whose request timed out, and dials a new one on the retry, while the next reading
// waits in the rule's buffer.
//
// It is a module of its own, to keep github.com/elojah/sunspec out of the library's go.mod: `go run -C
// examples/12-modbus .`. plc.go simulates the PLC, in process.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/elojah/sunspec/modbus"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/filter"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
	transport "github.com/elojah/broadcastor/transport/modbus"
)

const (
	// The PLC's unit, its temperature, in tenths of a degree, and its fan.
	unit                = 1
	temperatureRegister = 0
	fanCoil             = 0

	// The fan goes on above fanOn and off below fanOff, in tenths of a degree, so that a temperature hovering around
	// 30°C does not switch it at every poll.
	fanOn  = 300
	fanOff = 290
	// The trend keeps a reading once it has moved by deadband, in tenths of a degree.
	deadband = 10

	// A gateway would poll every second or so.
	pollEvery = 10 * time.Millisecond
	// A request the PLC has not answered by then fails with context.DeadlineExceeded.
	requestTimeout = 100 * time.Millisecond
)

type reading struct {
	Poll   int
	Tenths uint16
}

func (r reading) String() string {
	return fmt.Sprintf("poll %d, %.1f°C", r.Poll, float64(r.Tenths)/10)
}

func main() {
	ctx := context.Background()
	readings := []uint16{215, 216, 221, 232, 305, 309, 318, 296, 291, 284, 281}
	addr, stopPLC := servePLC(ctx, newPLC(readings))
	// A transport sends one request at a time, so the rule has its own: the poller never waits for its writes.
	poller := modbus.NewClient(modbus.NewTCPTransport(addr, modbus.WithTimeout(requestTimeout)))
	rule := modbus.NewClient(modbus.NewTCPTransport(addr, modbus.WithTimeout(requestTimeout)))

	b := broadcastor.NewBroadcastor[reading]()
	subscribeRule(ctx, b, rule)
	trend := store.NewRing[reading](1024)
	subscribeTrend(ctx, b, trend)
	poll(ctx, b, poller, len(readings))

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := b.Shutdown(shutdownCtx); err != nil {
		log.Fatal(err)
	}
	// Shutdown waited for the rule's last write.
	for _, client := range []*modbus.Client{rule, poller} {
		if err := client.Close(); err != nil {
			log.Fatal(err)
		}
	}
	if err := stopPLC(); err != nil {
		log.Fatal(err)
	}

	printTrend(ctx, trend)
}

// subscribeRule subscribes the fan rule, which writes the fan's coil with client.
func subscribeRule(ctx context.Context, b *broadcastor.Broadcastor[reading], client *modbus.Client) {
	handle := func(ctx context.Context, _ uint64, r reading) error {
		on := r.Tenths > fanOn
		// It sets the coil rather than toggling it, so a write that timed out but went through is safe to retry.
		if err := client.WriteCoil(ctx, unit, fanCoil, on); err != nil {
			return err
		}
		fmt.Printf("rule: %s, fan %s\n", r, onOff(on))

		return nil
	}

	retry := middleware.Retry[reading](middleware.RetryPolicy{
		Attempts: 5, Delay: 10 * time.Millisecond, Multiplier: 2, IsRetryable: transport.IsRetryable,
	})
	_, err := b.Subscribe(ctx, handle,
		// In parallel, so that the trend never waits for the rule.
		subscriber.WithDefaultMessageOptions(message.WithParallel[reading](), message.WithTimeout[reading](pollEvery)),
		// A reading that switches the fan waits there while the rule retries.
		subscriber.WithBuffer[reading](1),
		// The first reading passes, then each one that switches the fan from where the last one passed set it.
		subscriber.WithFilter(filter.Changed(func(prev, next reading) bool {
			if prev.Tenths > fanOn {
				return next.Tenths < fanOff
			}

			return next.Tenths > fanOn
		})),
		subscriber.WithMiddleware(retry),
		subscriber.WithErrorHandler[reading](logError),
	)
	if err != nil {
		log.Fatal(err)
	}
}

// subscribeTrend adds the trend, standing in for a time series database.
func subscribeTrend(ctx context.Context, b *broadcastor.Broadcastor[reading], trend *store.Ring[reading]) {
	_, err := b.Subscribe(ctx, store.Enqueue[reading](trend),
		subscriber.WithFilter(filter.Changed(func(prev, next reading) bool {
			return max(prev.Tenths, next.Tenths)-min(prev.Tenths, next.Tenths) >= deadband
		})),
		subscriber.WithErrorHandler[reading](logError),
	)
	if err != nil {
		log.Fatal(err)
	}
}

// poll broadcasts a reading of the temperature every pollEvery. A gateway would poll until SIGTERM, here n times, as
// many as the PLC has readings.
func poll(ctx context.Context, b *broadcastor.Broadcastor[reading], client *modbus.Client, n int) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	polls := 0
	read := func(ctx context.Context) (reading, error) {
		if polls == n {
			// Poll returns ctx's error, without broadcasting.
			stop()

			return reading{}, ctx.Err()
		}
		polls++
		registers, err := client.ReadRegisters(ctx, unit, modbus.InputRegisters, temperatureRegister, 1)
		if err != nil {
			return reading{}, err
		}

		return reading{Poll: polls, Tenths: registers[0]}, nil
	}

	if err := transport.Poll(ctx, b, pollEvery, read, logError); !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

// printTrend prints what the trend kept, oldest first.
func printTrend(ctx context.Context, trend *store.Ring[reading]) {
	for trend.Len() > 0 {
		entry, err := trend.Next(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("trend:", entry.Message)
		if err := trend.Ack(ctx, entry.ID); err != nil {
			log.Fatal(err)
		}
	}
}

func logError(_ context.Context, err error) {
	log.Println(err)
}

func onOff(on bool) string {
	if on {
		return "on"
	}

	return "off"
}
