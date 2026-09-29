package main

func Example() {
	main()

	// Output:
	// POST {"room":"kitchen","celsius":21.5} 202 Accepted
	// POST {"room":"garage","celsius":8} 202 Accepted
	// POST {"room":"kitchen","celsius":22} 202 Accepted
	// POST not json 400 Bad Request
	// event: kitchen
	// data: {"room":"kitchen","celsius":21.5}
	// event: kitchen
	// data: {"room":"kitchen","celsius":22}
}
