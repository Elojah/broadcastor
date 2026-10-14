package main

func Example() {
	main()

	// Unordered output:
	// display: north 20.1°C
	// display: south 18.0°C
	// display: east 19.5°C
	// display: probe 17.2°C
	// display: probe 17.4°C
	// display: probe 17.9°C
	// display: probe 18.1°C
	// uplink: one request for north 20.1°C, south 18.0°C, east 19.5°C
	// uplink: one request for probe 17.2°C, probe 17.4°C, probe 17.9°C
	// uplink: one request for probe 18.1°C
}
