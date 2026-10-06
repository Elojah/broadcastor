// A gateway polls a PLC over Modbus TCP for its temperature, and the bus fans each reading out to a fan rule and a
// trend. A poll returns the same value until it changes, so each subscriber filters what it wants with filter.Changed,
// before Broadcast wakes it up: the rule takes a reading only when the fan must switch, on above 30°C and off below
// 29°C, and the trend only once it has moved by 1°C since the last it kept.
//
// The rule writes the fan's coil over a connection of its own. The PLC answers its first write with exception 6,
// server device busy, as it is starting up, and middleware.Retry retries it: it retries a busy PLC or a request that
// timed out, not an exception a retry cannot cure, such as an illegal address. Broadcast waits for the rule a poll at
// most, so that the poll keeps its pace.
//
// Retry cures a request lost once, not a connection lost for good: the PLC never answers the rule's third write, as
// if a firewall in between had dropped the connection, idle between two switches of the fan. The write times out at
// each retry, so the next reading that switches the fan times out in Broadcast, which evicts the rule
// (subscriber.WithEvictAfter). Its onEvict closes the connection, and subscribes a new rule over a new one. The new
// rule has a new filter, which passes the next reading whatever it is, so the fan is set again at once.
//
// It is a module of its own, so that the library does not depend on a Modbus library: run it with
// `go run -C examples/25-modbus .`. It needs no device, since plc.go stands in for one, served in process.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
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
	poller := connect(addr)

	b := broadcastor.NewBroadcastor[reading]()
	// The rule's connection, which onEvict replaces.
	var ruleConn atomic.Pointer[modbus.ModbusClient]
	subscribeRule(ctx, b, addr, &ruleConn)
	trend := store.NewRing[reading](1024)
	subscribeTrend(ctx, b, trend)
	poll(ctx, b, poller, len(readings))

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := b.Shutdown(shutdownCtx); err != nil {
		log.Fatal(err)
	}
	for _, client := range []*modbus.ModbusClient{poller, ruleConn.Load()} {
		if err := client.Close(); err != nil {
			log.Fatal(err)
		}
	}
	if err := stopPLC(); err != nil {
		log.Fatal(err)
	}

	printTrend(ctx, trend)
}

// connect opens a connection to the PLC at addr.
func connect(addr string) *modbus.ModbusClient {
	client, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL:     "tcp://" + addr,
		Timeout: requestTimeout,
		// It logs to stdout without one.
		Logger: log.Default(),
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := client.Open(); err != nil {
		log.Fatal(err)
	}

	return client
}

// subscribeRule connects to the PLC, stores the connection in conn, and subscribes the fan rule, which writes the
// fan's coil over it. Once the rule is evicted, its onEvict closes the connection and calls subscribeRule again.
func subscribeRule(ctx context.Context, b *broadcastor.Broadcastor[reading], addr string, conn *atomic.Pointer[modbus.ModbusClient]) {
	client := connect(addr)
	conn.Store(client)

	retry := middleware.Retry[reading](middleware.RetryPolicy{
		Attempts: 5, Delay: 10 * time.Millisecond, Multiplier: 2,
		IsRetryable: func(err error) bool {
			return errors.Is(err, modbus.ErrServerDeviceBusy) || errors.Is(err, modbus.ErrRequestTimedOut)
		},
	})
	_, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, r reading) error {
		on := r.Tenths > fanOn
		if err := client.WriteCoil(fanCoil, on); err != nil {
			return err
		}
		fmt.Printf("rule: %s, fan %s\n", r, onOff(on))

		return nil
	},
		// In parallel, so that the trend never waits for the rule, and so that the Broadcast that evicts the rule is
		// done with every other subscriber once onEvict runs: one subscribed meanwhile might get its reading, or not.
		subscriber.WithDefaultMessageOptions(message.WithParallel[reading](), message.WithTimeout[reading](pollEvery)),
		// The rule takes a reading only to switch the fan, so the first it loses evicts it.
		subscriber.WithEvictAfter(1, func(_ context.Context, evicted *subscriber.EvictedError[reading]) {
			fmt.Printf("rule: evicted on losing %s, reconnecting\n", evicted.Message)
			// Close waits for the write under way to time out, and fails the rule's next retry.
			if err := client.Close(); err != nil {
				log.Println(err)
			}
			subscribeRule(ctx, b, addr, conn)
		}),
		// The first reading passes, then each one that switches the fan from where the last one passed set it, or
		// would have, had the rule not lost it.
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

// poll reads the temperature every pollEvery, and broadcasts each reading. A gateway would poll until SIGTERM. Here,
// n times, as many as the PLC has readings.
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

func logError(_ context.Context, err error) {
	log.Println(err)
}

func onOff(on bool) string {
	if on {
		return "on"
	}

	return "off"
}
