// A gateway takes readings over MQTT and fans each out to a rule, a local store, and an uplink. paho's message handler
// must not block, so it broadcasts with message.WithNonBlocking, to buffered subscribers. The uplink only queues each
// reading in an outbox (store.Enqueue), and store.Drain sends them in order once the link is back.
//
// It is a module of its own, to keep paho out of the library's go.mod: `go run -C examples/24-mqtt .` uses the broker
// at MQTT_ADDR, localhost:1883 by default (`docker run --rm -p 1883:1883 eclipse-mosquitto mosquitto -c
// /mosquitto-no-auth.conf`). Its test uses mochi-mqtt, in process.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

// Each sensor publishes to prefix + its name + suffix.
const (
	prefix = "broadcastor/examples/mqtt/sensors/"
	suffix = "/temperature"
	qos    = 1

	// quiesce is how long Disconnect waits for work in progress, in milliseconds.
	quiesce = 250
)

var errLinkDown = errors.New("uplink down")

type reading struct {
	Sensor  string    `json:"-"` // from the topic
	At      time.Time `json:"at"`
	Celsius int       `json:"celsius"`
}

func (r reading) String() string {
	return fmt.Sprintf("%s at %s, %d°C", r.Sensor, r.At.Format(time.TimeOnly), r.Celsius)
}

func at(seconds, celsius int) reading {
	return reading{"north", time.Date(2026, 10, 1, 10, 0, seconds, 0, time.UTC), celsius}
}

func main() {
	addr := os.Getenv("MQTT_ADDR")
	if addr == "" {
		addr = "localhost:1883"
	}
	run(addr)
}

func run(addr string) {
	ctx := context.Background()
	readings := []reading{at(0, 20), at(1, 31), at(2, 22), at(3, 33)}

	b := broadcastor.NewBroadcastor[reading]()
	local, outbox := store.NewRing[reading](1024), store.NewRing[reading](1024)
	subscribe(ctx, b, local, outbox)
	up := make(chan struct{})
	drained := uplink(ctx, outbox, up, len(readings))

	gateway := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + addr).SetClientID("broadcastor-example-gateway"))
	wait(gateway.Connect())
	var received sync.WaitGroup
	received.Add(len(readings))
	wait(gateway.Subscribe(prefix+"+"+suffix, qos, func(_ mqtt.Client, m mqtt.Message) {
		defer received.Done()
		r, err := decode(m)
		if err != nil {
			log.Println(err)

			return
		}
		b.Broadcast(ctx, r, message.WithNonBlocking[reading]())
	}))

	sensor := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + addr).SetClientID("broadcastor-example-sensor"))
	wait(sensor.Connect())
	for _, r := range readings {
		payload, err := json.Marshal(r)
		if err != nil {
			log.Fatal(err)
		}
		wait(sensor.Publish(prefix+r.Sensor+suffix, qos, false, payload))
	}
	sensor.Disconnect(quiesce)

	// A gateway would run until SIGTERM. Here, until the readings are in.
	received.Wait()
	gateway.Disconnect(quiesce)
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := b.Shutdown(shutdownCtx); err != nil {
		log.Fatal(err)
	}

	printLocal(ctx, local)

	fmt.Println("uplink back")
	close(up)
	if err := <-drained; !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

// subscribe adds the rule, the local store and the uplink, each with a buffer, since the gateway broadcasts without
// waiting: a reading that finds a buffer full is lost, as a *subscriber.DroppedError.
func subscribe(ctx context.Context, b *broadcastor.Broadcastor[reading], local, outbox *store.Ring[reading]) {
	options := []subscriber.Option[reading]{
		subscriber.WithBuffer[reading](64),
		subscriber.WithErrorHandler[reading](func(_ context.Context, err error) { log.Println(err) }),
	}
	handlers := []subscriber.Handler[reading]{
		func(_ context.Context, _ uuid.UUID, r reading) error {
			if r.Celsius > 30 {
				fmt.Println("rule: fan on,", r)
			}

			return nil
		},
		// Standing in for a database on the gateway's disk.
		store.Enqueue[reading](local),
		// Drain sends what the uplink queues.
		store.Enqueue[reading](outbox),
	}
	for _, handle := range handlers {
		if _, err := b.Subscribe(ctx, handle, options...); err != nil {
			log.Fatal(err)
		}
	}
}

// uplink sends what the outbox holds, oldest first, retrying each reading until sent, and returns what Drain returned.
// send stands in for a request to the cloud, failing until up is closed. A gateway would drain until it stops, here
// until it has sent n readings.
func uplink(ctx context.Context, outbox *store.Ring[reading], up <-chan struct{}, n int) <-chan error {
	sending, stop := context.WithCancel(ctx)
	send := func(_ context.Context, _ uuid.UUID, r reading) error {
		select {
		case <-up:
		default:
			return errLinkDown
		}
		fmt.Println("uplink: sent", r)
		if n--; n == 0 {
			stop()
		}

		return nil
	}
	retry := middleware.Retry[reading](middleware.RetryPolicy{
		Attempts: math.MaxInt, Delay: 10 * time.Millisecond, Multiplier: 2, MaxDelay: 100 * time.Millisecond,
	})

	drained := make(chan error, 1)
	go func() {
		defer stop()
		drained <- store.Drain(sending, outbox, retry(send), nil)
	}()

	return drained
}

// wait waits until t is done, which for a connection, a publish or a subscription is once the broker acknowledged it.
func wait(t mqtt.Token) {
	t.Wait()
	if err := t.Error(); err != nil {
		log.Fatal(err)
	}
}

// decode reads a reading from m, whose topic names the sensor.
func decode(m mqtt.Message) (reading, error) {
	var r reading
	if err := json.Unmarshal(m.Payload(), &r); err != nil {
		return reading{}, fmt.Errorf("%s: %w", m.Topic(), err)
	}
	r.Sensor = strings.TrimSuffix(strings.TrimPrefix(m.Topic(), prefix), suffix)

	return r, nil
}

// printLocal prints what the local store holds, oldest first.
func printLocal(ctx context.Context, local *store.Ring[reading]) {
	for local.Len() > 0 {
		entry, err := local.Next(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("local:", entry.Message)
		if err := local.Ack(ctx, entry.ID); err != nil {
			log.Fatal(err)
		}
	}
}
