package main

func Example() {
	main()

	// Output:
	// north at 10:00:00 handed to 0 subscribers
	// north at 10:00:01 handed to 0 subscribers
	// north at 10:00:02 handed to 0 subscribers
	// north at 10:00:03 handed to 0 subscribers
	// dashboard got north at 10:00:02
	// dashboard got north at 10:00:03
	// dashboard got north at 10:00:04
	// dashboard got north at 10:00:05
}
