package main

func Example() {
	main()

	// Output:
	// hello handed to 1 subscribers
	// request returned
	// got first (request none, ctx done: false)
	// got hello (request req-42, ctx done: false)
	// failed: unavailable (request req-42)
}
