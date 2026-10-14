package main

func Example() {
	main()

	// Output:
	// alerts failed to page north at 10:00:01, 31°C
	// alerts failed to page north at 10:00:03, 33°C
	// lost and not paged yet: 2
	// alerts paged north at 10:00:01, 31°C
	// alerts paged north at 10:00:03, 33°C
	// alerts paged north at 10:00:04, 34°C
	// lost and not paged yet: 0
}
