package main

func Example() {
	main()

	// Output:
	// send-email: attempt 1 failed: unavailable
	// send-email done
	// resize-image done
	// charge-card: attempt 1 failed: unavailable
	// charge-card: attempt 2 failed: unavailable
	// charge-card: attempt 3 failed: unavailable
	// gave up on charge-card: unavailable
}
