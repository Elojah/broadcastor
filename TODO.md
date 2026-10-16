# TODO

The library mostly runs on IoT gateways: little memory, sensors that repeat themselves, an uplink that drops out, power
that may go at any time. No new dependency in the library's go.mod. A performance change comes with `make benchcmp`, a
reliability fix with a test that fails without it. The API follows semver since v1.0.0: a breaking change waits for v2.

## Short-term

- [ ] Once TinyGo 0.43.0 is out, install its `.deb` in CI instead of the dev image, and update the README.
- [ ] Tag `transport/modbus/v0.1.0`, once sunspec has a tag to require instead of a pseudo-version.

## Mid-term

- [ ] `transport/mqtt`, on paho: `Subscribe` broadcasting with `message.WithNonBlocking`, since paho's handler must
  not block, and `Publish`, a `subscriber.Handler` waiting on the token's `Done()` or ctx. Then `11-mqtt` uses it.

## Long-term

- [ ] Write the delivery guarantees in `doc.go` as a contract, with a test for each, store and forward included (at
  least once).
- [ ] Run the examples on TinyGo's `cortex-m-qemu` target in `make tinygo-run`. CI then needs `qemu-system-arm`.
- [ ] Only once someone needs it: an observer hook on the counters, for a `broadcastor/otel` module.

## v2

Breaking changes, kept for a major version.

## Out of scope

- Topics and wildcards: a topic field with `WithFilter`, or one `Broadcastor` per topic, does it.
- Priority lanes: two `Broadcastor`s, one for alarms and one for telemetry, do it.
- Competing consumers: a `handle` feeding a pool of workers does it.
- MQTT, Redis or NATS clients in the library's module: each gets a v0 module under `transport/`, on the user's client.

## Manual personal notes
