module github.com/elojah/broadcastor/examples/12-modbus

go 1.26.1

require (
	github.com/elojah/broadcastor v1.0.0
	github.com/elojah/broadcastor/transport/modbus v0.0.0
	github.com/elojah/sunspec v0.0.0-20261015175300-58bcb27c100d
)

replace (
	github.com/elojah/broadcastor => ../..
	github.com/elojah/broadcastor/transport/modbus => ../../transport/modbus
)
