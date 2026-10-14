package main

import (
	"log"
	"log/slog"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
)

func Example() {
	// Its default logger writes to stdout.
	server := mochi.New(&mochi.Options{Logger: slog.New(slog.DiscardHandler)})
	if err := server.AddHook(new(auth.AllowHook), nil); err != nil {
		log.Fatal(err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "tcp", Address: "127.0.0.1:0"})
	if err := server.AddListener(tcp); err != nil {
		log.Fatal(err)
	}
	if err := server.Serve(); err != nil {
		log.Fatal(err)
	}

	run(tcp.Address())

	if err := server.Close(); err != nil {
		log.Fatal(err)
	}

	// Output:
	// rule: fan on, north at 10:00:01, 31°C
	// rule: fan on, north at 10:00:03, 33°C
	// local: north at 10:00:00, 20°C
	// local: north at 10:00:01, 31°C
	// local: north at 10:00:02, 22°C
	// local: north at 10:00:03, 33°C
	// uplink back
	// uplink: sent north at 10:00:00, 20°C
	// uplink: sent north at 10:00:01, 31°C
	// uplink: sent north at 10:00:02, 22°C
	// uplink: sent north at 10:00:03, 33°C
}
