// UDP in and out: udp.Receive broadcasts every datagram a sensor sends to the gateway, decoded with the address it came
// from, and a subscriber with udp.Send as handle forwards every reading to a dashboard. Receive never waits for a
// subscriber by default, so that one has a buffer. What decode fails on goes to the source's error handler.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/subscriber"
	"github.com/elojah/broadcastor/transport"
	"github.com/elojah/broadcastor/transport/udp"
)

var errEmpty = errors.New("empty datagram")

type reading struct {
	sensor string
	value  string
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	gateway, sensor, dashboard := listen(ctx), listen(ctx), listen(ctx)

	names := map[string]string{sensor.LocalAddr().String(): "sensor-1"}
	decode := func(b []byte, from net.Addr) (reading, error) {
		if len(b) == 0 {
			return reading{}, errEmpty
		}

		return reading{sensor: names[from.String()], value: string(b)}, nil
	}
	encode := func(r reading) ([]byte, net.Addr, error) {
		return fmt.Appendf(nil, "%s=%s", r.sensor, r.value), dashboard.LocalAddr(), nil
	}

	b := broadcastor.NewBroadcastor[reading]()
	if _, err := b.Subscribe(ctx, udp.Send(gateway, encode), subscriber.WithBuffer[reading](1)); err != nil {
		log.Fatal(err)
	}
	decodeErrors := make(chan error)
	received := make(chan error, 1)
	go func() {
		received <- udp.Receive(ctx, gateway, b, decode, transport.WithErrorHandler[reading](func(_ context.Context, err error) {
			decodeErrors <- err
		}))
	}()

	buf := make([]byte, 64)
	for _, value := range []string{"21.5", "", "22.0"} {
		if _, err := sensor.WriteTo([]byte(value), gateway.LocalAddr()); err != nil {
			log.Fatal(err)
		}
		if value == "" {
			fmt.Println("decode:", <-decodeErrors)

			continue
		}
		n, _, err := dashboard.ReadFrom(buf)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("dashboard got", string(buf[:n]))
	}

	cancel()
	fmt.Println("receive:", <-received)
	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
	for _, conn := range []net.PacketConn{gateway, sensor, dashboard} {
		if err := conn.Close(); err != nil {
			log.Fatal(err)
		}
	}
}

// listen returns a conn on a free loopback port.
func listen(ctx context.Context) net.PacketConn {
	conn, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}

	return conn
}
