package main

import (
	"log"

	"github.com/alicebob/miniredis/v2"
)

func Example() {
	server, err := miniredis.Run()
	if err != nil {
		log.Fatal(err)
	}
	defer server.Close()

	run(server.Addr())

	// Output:
	// alerts failed to page north at 10:00:01, 31°C
	// alerts failed to page north at 10:00:03, 33°C
	// alerts paged north at 10:00:01, 31°C
	// alerts paged north at 10:00:03, 33°C
	// north at 10:00:04, 21°C handed to 0 subscribers
	// dashboard got north at 10:00:00, 20°C
	// dashboard got north at 10:00:01, 31°C
	// dashboard got north at 10:00:02, 22°C
	// dashboard got north at 10:00:03, 33°C
	// dashboard got north at 10:00:04, 21°C
	// dashboard got north at 10:00:05, 34°C
}
