package main

func Example() {
	main()

	// Output:
	// plc: busy
	// rule: poll 1, 21.5°C, fan off
	// rule: poll 5, 30.5°C, fan on
	// rule: poll 10, 28.4°C, fan off
	// trend: poll 1, 21.5°C
	// trend: poll 4, 23.2°C
	// trend: poll 5, 30.5°C
	// trend: poll 7, 31.8°C
	// trend: poll 8, 29.6°C
	// trend: poll 10, 28.4°C
}
