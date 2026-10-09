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

## Short-term: small, no new invariant

- [ ] Refresh the baseline, which predates `Shutdown`, `WithFilter` and `WithDone`: a `Subscribe` and `Unsubscribe` pair
  now costs 13 allocations and 1070 B, against 11 and 737 B above.
- [ ] `Broadcastor.Send(ctx, id, msg, options...)`: hands msg to one subscriber through `Deliver`, so no new invariant,
  and returns whether it was taken, or a `*SubscriberNotFoundError`. For commands to one handler:
  `message.WithSubscriberFilter` reaches one too, but ranges over every subscriber.
- [ ] Measure what a subscriber costs in memory, idle and with `WithBuffer(n)`, under Go and TinyGo, and give the
  figures in the README, which only says that fitting a goroutine per subscriber is up to the program.
- [ ] Document in the README that `expvar.Publish("bus", expvar.Func(func() any { return b.Stats() }))` exposes
  `Stats` on `/debug/vars`.
- [ ] Once TinyGo 0.43.0 is out, install its `.deb` in CI instead of the dev image, and drop "a dev build until
  released" from the README.

## Mid-term: latest values, store and forward, operations

- [ ] Latest value wins: `subscriber.WithDropOldest()` for non-blocking sends, which drops the oldest buffered message
  instead of the new one, so that with `WithBuffer(1)` each subscriber holds one value. `Broadcast` then reads from
  `ch` too, and reports what it takes as a `*DroppedError`. First settle how `Stats` counts that message, already
  `Delivered`.
- [ ] `store.Latest(size, key func(T) K)`: a `Queue` whose `Put` replaces the entry with the same key, in its place,
  unless `Next` has handed it out. After an outage, `Drain` then sends each sensor's latest reading rather than every
  one. `WithDropOldest` conflates one stream in a buffer, this conflates per key, outside the core.
- [ ] `store.File`: a durable `store.Queue` on local disk, standard library only, for store and forward across reboots
  without Redis.
  - Append-only segments, a checksum per record so that a write torn by a power cut is dropped on reopen, and a file
    holding the ack offset. Fuzz reopening a truncated or corrupted segment.
  - Batched fsync, every n records or every interval, since flash wears with each write.
  - A size cap. Decide whether, once full, it drops the oldest segment or `Put` fails (a `StoreError`).
  - The caller gives the encoding (`func(T) ([]byte, error)` and its inverse), so it imports no codec.
  - `File.Replay(ctx) iter.Seq[T]`, like `19-redis`'s stream, so that `subscriber.WithReplay` hands a subscriber its
    dead letters from before a reboot.
  - [ ] A durable store keeps only `Record.Err`'s text, so `errors.Is(entry.Err, subscriber.ErrTimeout)` fails once
    read back. Decide whether `Record` carries the sentinel its error matches, before `store.File` sets its format.
- [ ] `store.DrainBatch(ctx, q, n, maxWait, handle)`: hands up to n entries at once, or fewer after maxWait, and acks
  them together, since cellular and LoRa uplinks pay per request. Measure it against `Drain` on `store.File`.
- [ ] `Shutdown` names the subscribers it still waits for once ctx is done: after `Close` they have left `Stats`, so a
  SIGTERM that times out says only `context.DeadlineExceeded`. `add` could keep each in a set that `drain` deletes it
  from on its ID, and `Shutdown` return a `*ShutdownError` with what is left, matching `ctx.Err()`.
- [ ] `NewBroadcastor(options...)` with `broadcastor.WithDefaultSubscriberOptions`, applied before each `Subscribe`'s
  own, so that one error handler, dead-letter store or `WithEvict` covers every subscriber. Package `filter`'s filters
  keep state, so it takes a func building the options per subscriber, or a shared filter mixes them up. Settle it
  before the freeze, since it changes `NewBroadcastor`.

## Long-term: v1.0, smaller targets

- [ ] Write the delivery guarantees in `doc.go` as a contract, with a test named after each, store and forward
  included: at least once, since a power cut between `handle` and `Ack` hands the entry again. Then freeze the API and
  follow semver.
- [ ] Settle before the freeze:
  - IDs as an atomic `uint64` counter instead of UUIDv7: no dependency, a smaller binary, `ErrClosed` the only error
    `Subscribe` returns, and a cheaper `Subscribe`. It breaks handle's signature, and only works because
    cross-process transports are out of scope.
  - `Unsubscribe`'s unused ctx: drop it, or make it wait for that subscriber's goroutine, like `Shutdown`.
- [ ] Run the examples on TinyGo's `cortex-m-qemu` target too, in `make tinygo-run`, so that they run on a
  microcontroller's runtime and heap rather than only build for one. CI then needs `qemu-system-arm`.
- [ ] Only once someone needs it: an observer hook where the counters are incremented, so that a `broadcastor/otel`
  module can export counters that survive unsubscribes.

## Out of scope

- Topics and wildcards in the core: a topic field with `WithFilter`, or one `Broadcastor` per topic, does it.
- Priority lanes: two channels per subscriber would double the invariants on closing and references. Two
  `Broadcastor`s, one for alarms and one for telemetry, do it.
- Competing consumers, each message to one subscriber of a group: a subscriber whose `handle` feeds a pool of workers
  does it, and order per subscriber would no longer hold.
- MQTT, Redis or NATS clients in the library: they go in example modules, each with its own go.mod.

## Manual personal notes

- Clean Shutdown and OnDone functions
- Explicit Suspend method ?
