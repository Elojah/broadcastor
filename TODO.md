# TODO

The library mostly runs on IoT gateways: little memory, sensors that send often and repeat themselves, an uplink that
drops out, power that may go at any time. The next steps bound memory, cut wake-ups and allocations, and keep messages
across restarts, with no new dependency in the library's go.mod. A performance change comes with `benchstat` against
`main` (`make benchcmp`), a reliability fix with a test that fails without it. Every change keeps the invariants in
CLAUDE.md and passes `make stress`.

## Baseline

At `21d751b` (Intel Core Ultra 5 226V, GOMAXPROCS 8), per subscriber and per `Broadcast`, for 1 to 1000 subscribers:

| Benchmark                                           | Time                     | B/op | Allocs |
| --------------------------------------------------- | ------------------------ | ---- | ------ |
| `BenchmarkBroadcast`, any mode, waiting on every op | ~1.3 µs                  |      |        |
| `BenchmarkBroadcast_Throughput`, sync               | 0.48–1.1 µs              | 48   | 1      |
| `BenchmarkBroadcast_Throughput`, parallel           | 1.8–2.4 µs               | ~290 | 3      |
| `BenchmarkBroadcast_Throughput`, async              | 1.4–1.5 µs, 3.8 µs for 1 | 144  | 2      |
| `BenchmarkBroadcast_Concurrent`                     | 0.82–1.2 µs              | 48   | 1      |
| `BenchmarkBroadcast_Churn`                          | 0.92–1.3 µs              |      |        |
| `BenchmarkSubscribeUnsubscribe`, per pair           | 2.1 µs                   | 737  | 11     |

## Short-term: bound memory, cut wake-ups

- [x] `subscriber.WithAsyncLimit(n)` and `Stats.Sending`.
- [x] No allocation for a `Broadcast` without options in sync, buffered and non-blocking modes.
- [x] `subscriber.WithFilter`, and package `filter` (`Changed`, `Every`).
- [x] `middleware.MaxAge` and `*subscriber.ExpiredError`.
- [x] `Stats.Handling`, for a watchdog.

## Mid-term: shutdown, latest values, store and forward

- [x] `Shutdown(ctx)`.
- [ ] Latest value wins: `subscriber.WithDropOldest()` for non-blocking sends, which drops the oldest buffered message
  instead of the new one. It suits state (readings, positions, config), and with `WithBuffer(1)` each subscriber holds
  one value. `Broadcast` then reads from `ch` too, and must report what it takes as a `*DroppedError`. First settle how
  `Stats` counts that message, already `Delivered`.
- [ ] `Broadcastor.Send(ctx, id, msg, options...)`: hands msg to one subscriber through `Deliver`, so no new invariant,
  and returns whether it was taken, or a `*SubscriberNotFoundError`. For commands to one handler. A late subscriber's
  current value needs no `Send`: set it and broadcast under a mutex, and subscribe with
  `subscriber.WithReplay(slices.Values([]T{current}))` under the same one, so no subscriber gets an older value after a
  newer one.
- [x] `subscriber.WithDone`.
- [x] Reconnecting, with no change to the core: in place, with `handle`, `middleware.Retry` and the buffer
  (`examples/26-reconnect`, `25-modbus`).
- [x] `subscriber.WithReplay(iter.Seq[T])`, which `19-redis` uses to replay its dead letters after a restart.
- [ ] `store.File`: a durable `store.Queue` on local disk, standard library only, for store and forward across reboots
  without Redis.
  - Append-only segments, a checksum per record so that a write torn by a power cut is dropped on reopen, and a file
    holding the ack offset.
  - Batched fsync, every n records or every interval, since flash wears with each write. A power cut loses at most a
    batch.
  - A size cap. Decide whether, once full, it drops the oldest segment or `Put` fails (a `StoreError`).
  - The caller gives the encoding (`func(T) ([]byte, error)` and its inverse), so it imports no codec.
  - [ ] A durable store keeps only `Record.Err`'s text, so `errors.Is(entry.Err, subscriber.ErrTimeout)` fails once
    read back. Decide whether `Record` carries the sentinel its error matches, before `store.File` sets its format.
- [x] `examples/24-mqtt`.

## Long-term: v1.0, smaller targets

- [ ] Write the delivery guarantees in `doc.go` as a contract, with a test named after each. Then freeze the API and
  follow semver.
- [ ] Settle before the freeze:
  - IDs as an atomic `uint64` counter instead of UUIDv7: no dependency, a smaller binary, `ErrClosed` the only error
    `Subscribe` returns, a cheaper `Subscribe`, and `Stats` in subscription order. It breaks handle's signature, and
    only works because cross-process transports are out of scope.
  - `Unsubscribe`'s unused ctx: drop it, or make it wait for that subscriber's goroutine, like `Shutdown`.
- [x] TinyGo: `make tinygo` in CI, on a pinned dev build until 0.43 (tinygo-org/tinygo#5692).
  - [ ] Once TinyGo 0.43.0 is out, install its `.deb` in CI instead of the dev image.
- [ ] `store.DrainBatch(ctx, q, n, maxWait, handle)`: hands up to n entries at once, or fewer after maxWait, and acks
  them together, since cellular and LoRa uplinks pay per request. Measure it against `Drain` once `store.File` exists.
- [ ] Only once someone needs it: an observer hook where the counters are incremented, so that a `broadcastor/otel`
  module can export counters that survive unsubscribes. Until then, document in the README that
  `expvar.Publish("bus", expvar.Func(func() any { return b.Stats() }))` exposes `Stats` on `/debug/vars`.

## Out of scope

- Topics and wildcards in the core: a topic field with `WithFilter`, or one `Broadcastor` per topic, does it.
- Priority lanes: two channels per subscriber would double the invariants on closing and references. Two
  `Broadcastor`s, one for alarms and one for telemetry, do it.
- MQTT, Redis or NATS clients in the library: they go in example modules, each with its own go.mod.

## Manual personal notes

- Clean Shutdown and OnDone functions
- Explicit Suspend method ?
