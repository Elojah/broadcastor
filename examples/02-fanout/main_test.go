package main

func Example() {
	main()

	// Unordered output:
	// alice got hello
	// bob got hello
	// hello handed to 2 subscribers
	// alice got bye
	// bob got bye
	// bye handed to 2 subscribers
}
