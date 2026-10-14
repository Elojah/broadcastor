package main

func Example() {
	main()

	// Unordered output:
	// display: south at 18.4°C
	// uplink: north at 20.2°C, latest of 3
	// uplink: south at 18.4°C, latest of 2
	// display: north at 21.5°C
	// uplink: north at 21.5°C, latest of 2
}
