# TODO

The library mostly runs on IoT gateways: little memory, sensors that repeat themselves, an uplink that drops out, power
that may go at any time. No new dependency in the library's go.mod. A performance change comes with `make benchcmp`, a
reliability fix with a test that fails without it. The API follows semver since v1.0.0: a breaking change waits for v2.

## Short-term

- [ ] Measure what a subscriber costs in memory, idle and with `WithBuffer(n)`, under Go and TinyGo, for the README.
- [ ] Document that `expvar.Publish("bus", expvar.Func(func() any { return b.Stats() }))` exposes `Stats`.

## Mid-term

- [ ] `subscriber.WithDropOldest()`: a non-blocking send drops the oldest buffered message instead of the new one, so
  that with `WithBuffer(1)` each subscriber holds the latest value. Settle first how `Stats` counts the message taken
  back, already `Delivered`.
- [ ] `store.Latest(size, key func(T) K)`: a `Queue` whose `Put` replaces the entry with the same key unless handed
  out, so that after an outage `Drain` sends each sensor's latest reading only.
- [ ] Holders measuring windows from a time the message carries (`at func(T) time.Time`, like `middleware.MaxAge`),
  rather than from when a busy subscriber takes it.
- [ ] `store.DrainBatch(ctx, q, n, maxWait, handle)`: hands up to n entries at once and acks them together, for uplinks
  that pay per request. handle is a `subscriber.Handler[message.Batch[T]]`, so one uplink serves both.

## Long-term

- [ ] Write the delivery guarantees in `doc.go` as a contract, with a test for each, store and forward included (at
  least once).
- [ ] Run the examples on TinyGo's `cortex-m-qemu` target in `make tinygo-run`. CI then needs `qemu-system-arm`.
- [ ] Only once someone needs it: an observer hook on the counters, for a `broadcastor/otel` module.

## v2

Breaking changes, kept for a major version.

- [ ] Settle `Unsubscribe`'s unused ctx: drop it, or wait for that subscriber like `Shutdown`.
- [ ] `NewBroadcastor(options...)` with `broadcastor.WithDefaultSubscriberOptions`, applied before each `Subscribe`'s
  own. Filters keep state, so it takes a func building the options per subscriber. A new parameter changes the type of
  `NewBroadcastor[T]` as a func value.
- [ ] `Shutdown` names the subscribers it still waits for once ctx is done, in a `*ShutdownError` matching
  `ctx.Err()`. Its godoc promises `ctx.Err()` itself, which `==` compares.
- [x] A subscriber's own counters in `Stats` (`subscriber.WithCounters`). `Stats.Counters` is a slice, so `Stats` no
  longer compares with `==`.

## Out of scope

- Topics and wildcards: a topic field with `WithFilter`, or one `Broadcastor` per topic, does it.
- Priority lanes: two `Broadcastor`s, one for alarms and one for telemetry, do it.
- Competing consumers: a `handle` feeding a pool of workers does it.
- MQTT, Redis or NATS clients in the library: they go in example modules.

## Manual personal notes
