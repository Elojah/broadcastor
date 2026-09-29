# TODO

The next steps make what exists faster and harder to break, rather than adding delivery modes. A performance change
lands with `benchstat` output against `main`, and a reliability fix with a test that fails without it. Every change
keeps the invariants in CLAUDE.md.

## Baseline

Measured at `21d751b` (Intel Core Ultra 5 226V, GOMAXPROCS 8), per subscriber and per `Broadcast`:

| Benchmark                                           | Time        | B/op | Allocs |
| --------------------------------------------------- | ----------- | ---- | ------ |
| `BenchmarkBroadcast`, any mode, waiting on every op | ~1.3 µs     |      |        |
| Sync, buffered subscribers, waiting once at the end | 0.55–0.8 µs | 48   | 1      |
| Parallel, same                                      | 1.5–2.2 µs  | ~290 | 3      |
| Async, same                                         | 1.2–2.1 µs  | 144  | 2      |
| `Subscribe` + `Unsubscribe`, per pair               | 2.7 µs      | 737  | 11     |

The last four rows come from a scratch benchmark, which the first item below adds.

## Short-term: measure, then fix what is local

Each item changes one function or adds one option. The first two sections make the others safe to do.

### Measurement

- [ ] Benchmarks that measure `Broadcast` itself. `BenchmarkBroadcast` waits for every subscriber on each op, so it
  times the wake-up of a parked goroutine (~1.3 µs per subscriber, buffered or not) and hides any change to
  `Broadcast`. Keep it for latency, and add:
  - throughput: buffered subscribers, waiting once at the end, reporting ns per delivery;
  - concurrent broadcasters (`b.RunParallel`) on the same subscribers, where `refs` and `delivered` are contended;
  - `Broadcast` while other goroutines subscribe and unsubscribe, where `sync.Map` has writes.
- [ ] `make benchcmp`: benchmark `main` and the working tree with `-count=10`, then compare them with `benchstat`.
- [ ] CI runs the benchmarks once (`-benchtime=1x`), since `go test` alone compiles them but never runs them.

### Testing

- [ ] CI runs `go test -race -shuffle=on -cpu 1,4`. With GOMAXPROCS 1, goroutines interleave differently from how they
  do on 8 cores, and `-shuffle` catches tests that depend on each other.
- [ ] A stress test, in real time with `-race`, of random interleavings from several goroutines: `Subscribe`,
  `SubscribeSeq`, `Unsubscribe` with and without discard, ctx cancellation, `Close`, and `Broadcast` in all four modes,
  with and without timeouts. Each `Broadcast` sends a unique number, and each subscriber records what handle and its
  error handler get. It checks that:
  - nothing panics, and every goroutine returns once everything is closed;
  - no subscriber gets a number twice, whether handled or reported;
  - a subscriber gets every number whose `Broadcast` started after its `Subscribe` returned and returned before its
    `Unsubscribe` was called, whether handled or reported;
  - `Stats` adds up once nothing is in flight.

  `make stress` runs it with `-count=50`, and CI once. Everything below relies on it.

### Reliability

- [ ] `Unsubscribe` frees the `Broadcast`s blocked on that subscriber. Today a `Broadcast` waiting on a stuck handle
  stays blocked after both `Unsubscribe` and `Close` (checked: still blocked 500 ms later), and so do async sends, until
  handle returns or their ctx ends. `Subscriber.Unsubscribe` would close a `done` channel, which only it closes, once,
  and `send` would select on it and report a `*ClosedError`. Messages already buffered are still handled with
  `WithUnsubscribeDeliver`, but a subscriber no longer takes new ones once unsubscribed, except for a send that races
  `Unsubscribe`. `ch` is still closed only by `release`. This is what makes `Close` and ctx-lifetime removal actually
  release a stuck subscriber.
- [ ] Bound async sends: `subscriber.WithAsyncLimit(n)`. Each async message to a stuck subscriber parks a goroutine
  that holds the message until the subscriber takes it or ctx ends. With `context.Background()` and no timeout, that
  may never happen, so the goroutines pile up without limit. Past n sends in flight, an async message is dropped with a
  `*DroppedError`. It needs an atomic counter per subscriber, which `Stats.Sending` exposes. Decide whether the default
  stays unlimited (today's behaviour) or becomes a bound, which is safer but drops messages silently when there is no
  error handler.
- [ ] `Stats.Handling`: how long the current handle has been running, 0 when idle. Today a stuck handle shows only
  indirectly, as `Queued == Buffer` with counters that stop moving. `Consume` already reads the clock before handle,
  so storing that time in an atomic is enough for a watchdog to unsubscribe a subscriber stuck for too long.

### Performance

- [ ] Parallel and async try the send in `Broadcast`'s goroutine first, and start a goroutine only for a subscriber
  that cannot take the message right away. Today they start one per subscriber per `Broadcast`, even for idle or
  buffered subscribers, which makes them 2–3× slower than sync when every subscriber keeps up. Parallel still returns
  once every subscriber has taken or missed the message, and async still never waits.
  - The goroutines left then report to one shared result per `Broadcast` (a count and a `WaitGroup`), instead of a
    `chan bool` each plus a growing `pending` slice.
- [ ] A `Broadcast` with no options allocates nothing in sync, buffered and non-blocking modes. Today it allocates
  48 B per subscriber: `message.New` passes `&config` to each option, so the config moves to the heap
  (`go build -gcflags=-m`: `moved to heap: message.config`, at the `message.New` call in `Deliver`). With no options,
  `Deliver` can build the message from the subscriber's defaults directly. With options, apply them once per
  `Broadcast` and record which fields they set in an unexported mask on `message.Config`, then merge those fields
  over each subscriber's defaults by value: one allocation per `Broadcast` instead of one per subscriber.

## Mid-term: slow subscribers and shutdown

- [ ] Evict slow subscribers with `subscriber.WithEvictAfter(n)`. Today a stuck subscriber with a timeout costs every
  sync `Broadcast` that full timeout, for as long as it stays subscribed. Redis evicts pub/sub clients over
  `client-output-buffer-limit` for the same reason, and NATS evicts slow consumers. `send` counts consecutive
  timeouts and drops, and a message taken resets the count. Once the count reaches n, the subscriber is unsubscribed
  with discard through `remove`, like every other removal. The loss that triggered the eviction is reported once, as
  an `*EvictedError` wrapping the original error, which matches `ErrEvicted`. It builds on the unblock item above.
- [ ] `Shutdown(ctx)`: `Close`, then wait until every subscriber's goroutine has returned (`Consume`, or a
  `SubscribeSeq` loop and its discard), or until ctx is done. `Close` never waits, so today a program that exits right
  after it cuts off whatever handle was doing, and cannot flush the buffers first. With `WithUnsubscribeDeliver`,
  `Shutdown` flushes the buffers. With discard, it reports what is left. The count goes up in `add` under the gate, so
  none goes up after `Close`. `Shutdown` waits for a `Broadcast` only through a subscriber, which the unblock item
  frees quickly, so it is the one documented, ctx-bounded exception to "nothing waits for a `Broadcast`". Called from
  handle, it waits on itself until ctx is done. So is a `SubscribeSeq` loop that was never started.
- [ ] Latest value wins: `subscriber.WithDropOldest()` for non-blocking sends. When the buffer is full, it drops the
  oldest message to make room instead of the new one. That suits state broadcasts (prices, positions, config), where a
  slow subscriber wants the latest message rather than the first. `Broadcast` then reads from `ch` too, so it must
  report what it takes as a `*DroppedError`. Settle how `Stats` counts that message, since it was already `Delivered`,
  before writing any code. This is all that remains of "ordered async": `WithBuffer` and `WithNonBlocking` already give
  ordered, bounded delivery that never waits and drops the newest message, with no sender goroutine.
- [ ] `subscriber.WithFilter(func(T) bool)`, for performance. The filter runs in `Broadcast`'s goroutine before
  `acquire`, so a message the subscriber skips costs a func call instead of a wake-up (~1 µs). It is neither reported
  nor counted, including in `Broadcast`'s return value. It is the cheapest way to cut the cost of subscribers that want
  a subset of the messages.
- [ ] Measure first, then decide:
  - A copy-on-write `atomic.Pointer[[]*subscriber.Subscriber[T]]` instead of `sync.Map`. It iterates faster, in
    subscription order, and `Stats` no longer has to sort. The cost is an O(n) copy under a mutex on each `Subscribe`
    and `Unsubscribe`. Only worth it if `Range` shows in the throughput or concurrent-broadcaster profiles.
  - Padding `refs` and the counters `Broadcast` writes away from those the subscriber's goroutine writes, if the
    concurrent-broadcaster benchmark shows false sharing.

## Long-term: v1.0

- [ ] Write the delivery guarantees in `doc.go` as a contract, with one test named after each guarantee. Then freeze
  the API and follow semver.
- [ ] Settle before the freeze:
  - IDs as an atomic `uint64` counter instead of UUIDv7. That removes the only dependency, makes `ErrClosed` the only
    error `Subscribe` can return, cuts the cost of `Subscribe`, and keeps `Stats` in subscription order. It breaks
    handle's signature, and it is only possible because cross-process transports are out of scope (see below).
  - `Unsubscribe`'s ctx, which is unused: drop it, or make it wait for that one subscriber's goroutine, like
    `Shutdown`.
- [ ] Check that `store.Queue` works with a durable backend (SQLite, in a scratch branch) before freezing it. That is
  what store-and-forward across restarts relies on.
- [ ] Only once someone needs it: an observer hook in the core, where the counters are incremented, so that a separate
  `broadcastor/otel` module can export counters that keep counting after unsubscribes.

## Out of scope

- Consumer groups, topics and wildcards, replay for late subscribers, and batching are features built on top of the
  fan-out, and handle, `SubscribeSeq` or a store can build them in user code. `WithFilter` is the only part kept,
  because it saves wake-ups.
- An error channel or `WithErrorStorage`: an error handler that puts errors into a `store.Ring` already does it.
- Ordered async as a mode of its own: see `WithDropOldest` above.
- At-least-once delivery with acks and visibility timeouts, and transports such as Redis, NATS or Postgres. Those make
  a different library. This one stays in-process, and `store.Queue` is where durability plugs in.
