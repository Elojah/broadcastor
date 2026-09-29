package main

func Example() {
	main()

	// Output:
	// broadcast a
	// broadcast b
	// broadcast c
	// uplink down, retrying
	// uplink up
	// sent a
	// sent b
	// sent c
	// drain: context canceled
}
