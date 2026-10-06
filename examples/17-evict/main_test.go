package main

func Example() {
	main()

	// Output:
	// 1 handed to 2 subscribers
	// stuck lost 2, evicted: false
	// 2 handed to 1 subscribers
	// stuck lost 3, evicted: true
	// stuck evicted on losing 3
	// 3 handed to 1 subscribers
	// 4 handed to 1 subscribers
	// subscribers left: 1
}
