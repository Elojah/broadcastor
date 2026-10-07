# TODO

The library is mostly used in IoT: gateways with little memory, sensors that send often and repeat themselves, an uplink
that drops out, and power that may go at any time. The next steps bound memory, cut wake-ups and allocations, and keep
messages across restarts, without adding a dependency to the library's go.mod. A performance change lands with
`benchstat` output against `main` (`make benchcmp`), and a reliability fix with a test that fails without it. Every
change keeps the invariants in CLAUDE.md, and passes `make stress`.

## Baseline

Measured with the library as of `21d751b` (Intel Core Ultra 5 226V, GOMAXPROCS 8), per subscriber and per `Broadcast`,
for 1 to 1000 subscribers:

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

Each item changes one function or adds one option.

- [x] Bound async sends: `subscriber.WithAsyncLimit(n)`. Each async message to a stuck subscriber parks a goroutine
  that holds the message until the subscriber takes it or ctx ends. With `context.Background()` and no timeout, that
  may never happen, so the goroutines pile up without limit: on a gateway with little memory, the likeliest way to run
  out of it. Past n sends in flight, an async message is dropped with a `*DroppedError`. It needs an atomic counter per
  subscriber, which `Stats.Sending` exposes. Lean towards keeping the default unlimited until v1.0: a bound drops
  messages silently when there is no error handler, whereas `Stats.Sending` shows a pile-up.
- [x] A `Broadcast` with no options allocates nothing in sync, buffered and non-blocking modes. Today it allocates
  48 B per subscriber, which at a sensor's rate is garbage on every reading: `message.New` passes `&config` to each
  option, so the config moves to the heap (`go build -gcflags=-m`: `moved to heap: message.config`, at the
  `message.New` call in `Deliver`). With no options, `Deliver` can build the message from the subscriber's defaults
  directly. With options, apply them once per `Broadcast` and record which fields they set in an unexported mask on
  `message.Config`, then merge those fields over each subscriber's defaults by value: one allocation per `Broadcast`
  instead of one per subscriber.
- [x] `subscriber.WithFilter(func(T) bool)`. The filter runs in `Broadcast`'s goroutine before `acquire`, so a message
  the subscriber skips costs a func call instead of a wake-up (~1 µs). It is neither reported nor counted, including in
  `Broadcast`'s return value.
  - [x] A `filter` package, importing nothing from the library, for readings that repeat themselves:
    `filter.Changed(func(prev, next T) bool)` passes a reading only once it moved past a threshold (a deadband), and
    `filter.Every(d)` passes at most one per period. Concurrent `Broadcast`s may call a filter at once, so these keep
    their state in an atomic or behind a mutex, and "since the last one" is then approximate.
- [x] `middleware.MaxAge(d, func(T) time.Time)`: a message older than d when handle would get it is not handled, since
  a reading or a command that waited behind a slow handle may be worse than none. The time comes from the message,
  since the library stamps none. It returns a `*subscriber.ExpiredError` (`subscriber.ErrExpired`), so the message
  counts as `Failed` and reaches the dead letters like any handle error. Settle where it goes in the recommended order:
  before `Retry`, so that an expired message is not retried.
- [x] `Stats.Handling`: how long the current handle has been running, 0 when idle. Today a stuck handle (a hung serial
  or I²C read) shows only indirectly, as `Queued == Buffer` with counters that stop moving. `Consume` already reads the
  clock before handle, so storing that time in an atomic is enough for a watchdog to unsubscribe a subscriber stuck
  for too long.

## Mid-term: shutdown, latest values, store and forward

- [x] `Shutdown(ctx)`: `Close`, then wait until every subscriber's goroutine has returned (`Consume`, which a
  `SubscribeSeq` loop runs too), or until ctx is done. A device gets SIGTERM, or a power-fail signal, shortly before
  it goes down, but `Close` never waits, so a program that exits right after it cuts off whatever handle was doing,
  and cannot flush the buffers first. With `WithUnsubscribeDeliver`, `Shutdown` flushes the buffers. With discard, it
  reports what is left, to the dead letters. The count goes up in `add` under the gate, so none goes up after `Close`.
  `Shutdown` waits for a `Broadcast` only through a subscriber, which `Unsubscribe` frees right away, so it is the one
  documented, ctx-bounded exception to "nothing waits for a `Broadcast`". Called from handle, it waits on itself until
  ctx is done. So is a `SubscribeSeq` loop that was never started.
- [ ] Latest value wins: `subscriber.WithDropOldest()` for non-blocking sends. When the buffer is full, it drops the
  oldest message to make room instead of the new one. That suits state (readings, positions, config), where a slow
  subscriber wants the latest message rather than the first, and with `WithBuffer(1)` each subscriber holds one value.
  `Broadcast` then reads from `ch` too, so it must report what it takes as a `*DroppedError`. Settle how `Stats` counts
  that message, since it was already `Delivered`, before writing any code.
- [ ] `Broadcastor.Send(ctx, id, msg, options...)`: hands msg to one subscriber, through the same `Deliver` as
  `Broadcast`, so it needs no new invariant, and returns whether it took it, or a `*SubscriberNotFoundError`. It sends
  a command to one handler. A late subscriber's current value, like an MQTT retained message, needs no `Send` now:
  the wrapper sets the value and broadcasts under a mutex, and subscribes with
  `subscriber.WithReplay(slices.Values([]T{current}))` under the same one, so no subscriber gets an older value after
  a newer one, and `Subscribe` never waits under the mutex.
- [x] `subscriber.WithOnDone(onDone)`: runs once the subscriber is done, however it was removed (`Unsubscribe`,
  `Close`, its ctx, eviction), in its goroutine after handle's last call, so that a subscriber owning a resource
  releases it then, without a lock. `Shutdown` waits for it: `running` drops through the last `onDone`, which `add`
  passes after the caller's, so `Attach` no longer takes `exit`. `examples/25-modbus` still keeps the rule's
  connection in an `atomic.Pointer`, until reconnecting is redesigned.
  - [ ] Settle what a subscriber added during a `Broadcast` gets: `sync.Map.Range` may visit it or not, so a
    replacement that `onEvict` subscribes from a sync `Broadcast` gets the message that evicted the old one, or not
    (13 runs of 30 in `examples/25-modbus` without `message.WithParallel`, which it uses for that reason). Either
    document it on `Subscribe` and `WithEvictAfter`, or have `Broadcast` skip subscribers added after it started: a
    sequence number set in `add`, against one `Broadcast` reads first, for an atomic load per `Broadcast`. The latter
    lets `25-modbus` drop `WithParallel`.
- [x] `subscriber.WithReplay(replay iter.Seq[T])`: values the subscriber handles before any `Broadcast`'s message,
  through its middlewares, error handlers and dead letters like any other. `examples/19-redis` replays its dead letters
  after a restart instead of running `store.Drain` with a handle of its own. Settled:
  - The iterator acks, once `yield` returns, which is once the value is handled or reported. `yield` returns false,
    without handling the value, once the subscriber discards, and the rest stays in the source. Without discard, an
    unsubscribed subscriber goes on replaying, and `Shutdown` waits for it.
  - `Consume` replays before it reads `ch`, so neither `Subscribe` nor `onEvict` waits, and a `Broadcast` meanwhile
    waits for it as for a busy handle, or fills its buffer.
  - A value lost again goes back to the dead letters, so an iterator that reads them yields only what it read up
    front, as `19-redis`'s `stream.Replay` does.
  - `Stats` counts a replayed value in `Delivered`, then `Handled` or `Failed`, and never towards `WithEvictAfter`.
  - The `Broadcastor.Send` item above no longer needs `Send` for a late subscriber's current value.
- [ ] `store.File`: a durable `store.Queue` on local disk, with the standard library only, for store and forward across
  reboots where no Redis runs.
  - Append-only segment files, a checksum per record so that a write torn by a power cut is dropped on reopen, and a
    file holding how far the reader acked.
  - Batched fsync, every n records or every interval, since flash and SD cards wear with each write. A power cut
    loses at most that batch.
  - A size cap. Decide whether, once full, it drops the oldest segment or `Put` fails (a `StoreError`).
  - The caller gives the encoding (`func(T) ([]byte, error)` and its inverse), so it imports no codec.
  - [ ] A durable store keeps only the text of `Record.Err`, so `errors.Is(entry.Err, subscriber.ErrTimeout)` no
    longer holds once read back. Decide whether `Record` should also carry which sentinel its error matches, before
    `store.File` sets its format.
- [x] `examples/24-mqtt`, a module of its own like `19-redis` (listed in `MODULES` and the README): MQTT in, then fan
  out to a local rule, a local store, and an uplink through `store.Enqueue` and `store.Drain`. Its `Example()` runs
  against an in-process broker (such as mochi-mqtt), so CI needs none.

## Long-term: v1.0, smaller targets

- [ ] Write the delivery guarantees in `doc.go` as a contract, with one test named after each guarantee. Then freeze
  the API and follow semver.
- [ ] Settle before the freeze:
  - IDs as an atomic `uint64` counter instead of UUIDv7. That removes the only dependency, which also shrinks the
    binary, makes `ErrClosed` the only error `Subscribe` can return, cuts the cost of `Subscribe`, and keeps `Stats` in
    subscription order. It breaks handle's signature, and it is only possible because cross-process transports are
    out of scope.
  - `Unsubscribe`'s ctx, which is unused: drop it, or make it wait for that one subscriber's goroutine, like
    `Shutdown`.
- [x] Try TinyGo: `make tinygo`, a CI job of its own, runs the examples on Linux and builds them for a Pico. Nothing
  broke in the library: `sync.Map`, `context.AfterFunc` and `iter` work. TinyGo 0.42's `sync.RWMutex` deadlocks
  `pkg/gate` (tinygo-org/tinygo#5692), so CI uses a pinned dev build. Whether a goroutine per subscriber fits a
  microcontroller's RAM is not measured, since nothing runs on the Pico.
  - [ ] Once TinyGo 0.43.0 is out, install its `.deb` in CI instead of the dev image.
- [ ] `store.DrainBatch(ctx, q, n, maxWait, handle)`: hands up to n entries at once, or fewer once maxWait has passed,
  and acks them together, since cellular and LoRa uplinks pay per request. Once `store.File` exists, to measure it
  against `Drain`.
- [ ] Only once someone needs it: an observer hook in the core, where the counters are incremented, so that a separate
  `broadcastor/otel` module can export counters that keep counting after unsubscribes. Until then, document in the
  README that `expvar.Publish("bus", expvar.Func(func() any { return b.Stats() }))` exposes `Stats` on
  `/debug/vars`.

## Out of scope

- Topics and wildcards in the core: a topic field with `WithFilter`, or one `Broadcastor` per topic, does it.
- Priority lanes: two channels per subscriber would double the invariants on closing and references. Two
  `Broadcastor`s, one for alarms and one for telemetry, do it.
- MQTT, Redis or NATS clients in the library. They go in example modules, each with its own go.mod.
