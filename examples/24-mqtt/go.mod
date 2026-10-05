module github.com/elojah/broadcastor/examples/24-mqtt

go 1.26.1

require (
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/elojah/broadcastor v0.1.0
	github.com/google/uuid v1.6.0
	github.com/mochi-mqtt/server/v2 v2.7.9
)

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/rs/xid v1.4.0 // indirect
	golang.org/x/net v0.44.0 // indirect
	golang.org/x/sync v0.17.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/elojah/broadcastor => ../..
