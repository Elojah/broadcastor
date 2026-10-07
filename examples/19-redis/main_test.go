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
	// alerts paged north at 10:00:04, 34°C
	// lost and not paged yet: 0
	// history: north at 10:00:00, 20°C
	// history: north at 10:00:01, 31°C, lost: pager unreachable
	// history: north at 10:00:02, 22°C
	// history: north at 10:00:03, 33°C, lost: pager unreachable
	// history: north at 10:00:01, 31°C
	// history: north at 10:00:03, 33°C
	// history: north at 10:00:04, 34°C
}
