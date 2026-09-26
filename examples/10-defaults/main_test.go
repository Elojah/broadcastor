package main

func Example() {
	main()

	// Unordered output:
	// log: started
	// log: 50%
	// dashboard missed: 50%
	// dashboard: started
	// log: done
	// dashboard: done
}
