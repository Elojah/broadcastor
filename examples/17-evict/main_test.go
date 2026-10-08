package main

func Example() {
	main()

	// Output:
	// sensor failed: reading 3: unplugged
	// sensor failed: reading 4: unplugged
	// sensor evicted: failed in a row: reading 5: unplugged
	// 6 handed to 2 subscribers
	// stuck lost 7, evicted: true
	// 7 handed to 1 subscribers
	// 8 handed to 1 subscribers
	// stuck evicted on losing 7
	// subscribers left: 1
}
