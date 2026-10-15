module github.com/elojah/broadcastor/examples/12-modbus

go 1.26.1

require (
	github.com/elojah/broadcastor v1.0.0
	github.com/simonvetter/modbus v1.6.4
)

require github.com/goburrow/serial v0.1.0 // indirect

replace github.com/elojah/broadcastor => ../..
