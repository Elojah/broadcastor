package main

func Example() {
	main()

	// Output:
	// serial dropped 4
	// serial: stuck in handle for more than 10ms, 2 async sends waiting, unsubscribed
	// logger: ok
	// subscribers left: 1
}
