// A gateway polls a PLC's temperature over Modbus TCP and fans it out to a fan rule and a trend, each filtering the
// repeated readings with filter.Changed. The rule writes the fan's coil over its own connection, and middleware.Retry
// retries a busy PLC or a timed-out request. When the PLC stops answering, the rule reconnects in place, while the next
// reading waits in its buffer.
//
// It is a module of its own, to keep simonvetter/modbus out of the library's go.mod: `go run -C examples/25-modbus .`.
// plc.go simulates the PLC, in process.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/simonvetter/modbus"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/filter"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

const (
	// The PLC's temperature, in tenths of a degree, and its fan.
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
	// A request the PLC has not answered by then fails with modbus.ErrRequestTimedOut.
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
	poller, err := connect(addr)
	if err != nil {
		log.Fatal(err)
	}

	b := broadcastor.NewBroadcastor[reading]()
	closeRule := subscribeRule(ctx, b, addr)
	trend := store.NewRing[reading](1024)
	subscribeTrend(ctx, b, trend)
	poll(ctx, b, poller, len(readings))

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := b.Shutdown(shutdownCtx); err != nil {
		log.Fatal(err)
	}
	closeRule()
	if err := poller.Close(); err != nil {
		log.Fatal(err)
	}
	if err := stopPLC(); err != nil {
		log.Fatal(err)
	}

	printTrend(ctx, trend)
}

// connect opens a connection to the PLC at addr.
func connect(addr string) (*modbus.ModbusClient, error) {
	client, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL:     "tcp://" + addr,
		Timeout: requestTimeout,
		// It logs to stdout without one.
		Logger: log.Default(),
	})
	if err != nil {
		return nil, err
	}
	if err := client.Open(); err != nil {
		return nil, err
	}

	return client, nil
}

// subscribeRule subscribes the fan rule, which writes the fan's coil over its own connection, and opens a new one after
// a timeout. It returns a func that closes the last connection once the rule is done.
func subscribeRule(ctx context.Context, b *broadcastor.Broadcastor[reading], addr string) func() {
	// Only the rule's goroutine uses it in handle, and the returned func once done has the ID, so it needs no lock. nil
	// once closed, until the next attempt.
	client, err := connect(addr)
	if err != nil {
		log.Fatal(err)
	}
	handle := func(_ context.Context, _ uuid.UUID, r reading) error {
		if client == nil {
			fmt.Println("rule: reconnecting")
			c, err := connect(addr)
			if err != nil {
				return err
			}
			client = c
		}
		on := r.Tenths > fanOn
		if err := client.WriteCoil(fanCoil, on); err != nil {
			if errors.Is(err, modbus.ErrRequestTimedOut) {
				// The connection may be gone for good: Retry's next attempt opens a new one.
				closeClient(client)
				client = nil
			}

			return err
		}
		fmt.Printf("rule: %s, fan %s\n", r, onOff(on))

		return nil
	}

	retry := middleware.Retry[reading](middleware.RetryPolicy{
		Attempts: 5, Delay: 10 * time.Millisecond, Multiplier: 2,
		IsRetryable: func(err error) bool {
			return errors.Is(err, modbus.ErrServerDeviceBusy) || errors.Is(err, modbus.ErrRequestTimedOut)
		},
	})
	// Room for the rule's ID, since sending never waits.
	done := make(chan uuid.UUID, 1)
	_, err = b.Subscribe(ctx, handle,
		// In parallel, so that the trend never waits for the rule.
		subscriber.WithDefaultMessageOptions(message.WithParallel[reading](), message.WithTimeout[reading](pollEvery)),
		// A reading that switches the fan waits there while the rule reconnects.
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
		subscriber.WithDone[reading](done),
	)
	if err != nil {
		log.Fatal(err)
	}

	return func() {
		<-done // sent after handle's last call
		if client != nil {
			closeClient(client)
		}
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
func poll(ctx context.Context, b *broadcastor.Broadcastor[reading], client *modbus.ModbusClient, n int) {
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for i := 1; i <= n; i++ {
		<-ticker.C
		tenths, err := client.ReadRegister(temperatureRegister, modbus.INPUT_REGISTER)
		if err != nil {
			// The next poll brings a newer reading.
			log.Println(err)

			continue
		}
		b.Broadcast(ctx, reading{Poll: i, Tenths: tenths})
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

// closeClient closes client, and logs why it failed to.
func closeClient(client *modbus.ModbusClient) {
	if err := client.Close(); err != nil {
		log.Println(err)
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
