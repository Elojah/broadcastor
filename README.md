<p align="center">
  <img src="assets/logo.png" alt="broadcastor" width="180">
</p>

# broadcastor

[![CI](https://github.com/Elojah/broadcastor/actions/workflows/ci.yml/badge.svg)](https://github.com/Elojah/broadcastor/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/elojah/broadcastor.svg)](https://pkg.go.dev/github.com/elojah/broadcastor)

A generic in-process fan-out for Go: one `Broadcast` hands a message to every subscriber, and each subscriber handles
its messages one at a time, in its own goroutine. No dependencies, and it runs on TinyGo.

```sh
go get github.com/elojah/broadcastor
```

## Quick start

```go
b := broadcastor.NewBroadcastor[string]()

id, err := b.Subscribe(ctx, func(ctx context.Context, id uint64, msg string) error {
 fmt.Println(id, "got", msg)
 return nil
}, subscriber.WithErrorHandler[string](func(ctx context.Context, err error) {
 log.Println(err)
}))
if err != nil {
 return err
}

n := b.Broadcast(ctx, "hello") // how many subscribers took it

err = b.Unsubscribe(ctx, id)
```

`SubscribeSeq` returns an iterator instead, whose loop body replaces `handle`. Breaking out unsubscribes:

```go
_, seq, err := b.SubscribeSeq(ctx)
...
for msg, fail := range seq {
 fail(save(msg)) // nil, or never calling fail, means handled
}
```

| Package | Holds |
| --- | --- |
| [`broadcastor`](https://pkg.go.dev/github.com/elojah/broadcastor) | `Broadcastor`. |
| [`subscriber`](https://pkg.go.dev/github.com/elojah/broadcastor/subscriber) | Subscriber options, error types, `Stats`, `Holder`. |
| [`message`](https://pkg.go.dev/github.com/elojah/broadcastor/message) | `Broadcast` options, and `Batch` with `Slice` and `Map`. |
| [`middleware`](https://pkg.go.dev/github.com/elojah/broadcastor/middleware) | `Recover`, `History`, `MaxAge`, `WrapError`, `Retry`. |
| [`store`](https://pkg.go.dev/github.com/elojah/broadcastor/store) | Queues for dead letters: `Ring` in memory, `File` on disk, `Drain`, `Enqueue`, `Filter`. |
| [`holder`](https://pkg.go.dev/github.com/elojah/broadcastor/holder) | Holders: `Reduce`, `NewLatest`, `Reorder`, `Jitter`, `Batch`. |
| [`filter`](https://pkg.go.dev/github.com/elojah/broadcastor/filter) | Stateful filters: `Changed`, `Every`. |

## Delivery

The mode is a message option, passed to `Broadcast` or set as a subscriber's default with
`subscriber.WithDefaultMessageOptions`.

| Mode | Option | `Broadcast` waits for |
| --- | --- | --- |
| Sync (default) | `message.WithSync` | Each subscriber in turn. |
| Buffered | `subscriber.WithBuffer(n)` | Nothing until the buffer is full, then as sync. |
| Parallel | `message.WithParallel` | Every subscriber at once. |
| Async | `message.WithAsync` | Nothing: each send runs in its own goroutine, out of order. |
| Non-blocking | `message.WithNonBlocking` | Nothing: a busy subscriber misses the message. |

Delivery is at most once, in broadcast order from one goroutine, except async. `Broadcast` gives up on a subscriber
once its ctx is done or the message's timeout runs out (`message.WithTimeout`, `subscriber.WithTimeout`), and the error
handlers learn why. `subscriber.WithAsyncLimit(n)` bounds the async sends waiting on a subscriber.

`subscriber.WithFilter(keep)` skips what `keep` rejects before any send. `message.WithSubscriberFilter(keep)` hands one
message only to the subscribers `keep` picks by ID. Package `filter` holds stateful filters, so give each subscriber
its own:

```go
// Once the temperature has moved by half a degree since the last one passed, and at most once a minute.
id, err := b.Subscribe(ctx, display, subscriber.WithFilter(filter.Changed(func(prev, next float64) bool { return math.Abs(next-prev) >= 0.5 })))
id, err = b.Subscribe(ctx, uplink, subscriber.WithFilter(filter.Every[float64](time.Minute)))
```

## Errors

Errors go to `subscriber.WithErrorHandler`, then to `message.WithErrorHandler`. Without either, they are discarded.

| Error | When |
| --- | --- |
| `handle`'s error, as is | `handle`, its middlewares, or `fail` for `SubscribeSeq`, failed. |
| `*subscriber.HandleError` | The same, wrapped by `middleware.WrapError`. |
| `*subscriber.PanicError` | `middleware.Recover` recovered a panic. Without it, the program crashes. |
| `*subscriber.ExpiredError` | `middleware.MaxAge` found the message too old. |
| `*subscriber.TimeoutError` | `Broadcast` gave up waiting. |
| `*subscriber.DroppedError` | A non-blocking or async `Broadcast`, or a holder, had no room. |
| `*subscriber.ClosedError` | The subscriber was unsubscribed before handling the message. |
| `*subscriber.EvictedError` | Wraps the error that evicted the subscriber (`subscriber.WithEvict`). |
| `*subscriber.StoreError` | Wraps a loss the dead-letter store failed to keep. |

Each matches a sentinel with `errors.Is` (`subscriber.ErrTimeout`, …) without needing the message type.

## Middleware

`subscriber.WithMiddleware` wraps `handle`, the first middleware outermost, in the subscriber's goroutine:

```go
id, err := b.Subscribe(ctx, handle, subscriber.WithMiddleware(
 middleware.Recover[string](),                   // a panic becomes a *subscriber.PanicError
 middleware.History[string](history),            // each message handled is put in history
 middleware.MaxAge[string](time.Minute, sentAt), // an older message becomes a *subscriber.ExpiredError
 middleware.WrapError[string](),                 // an error becomes a *subscriber.HandleError
 middleware.Retry[string](middleware.RetryPolicy{Attempts: 3, Delay: 10 * time.Millisecond, Multiplier: 2}),
))
```

Keep that order: `Recover` first catches panics in every later middleware, and `Retry` after `WrapError` retries raw
errors. With `SubscribeSeq`, middlewares wrap the loop body, but `Recover` cannot catch a panic in it.

## Holding and batches

`subscriber.WithHolder` holds each message the subscriber takes until the holder releases it to `handle`, so that a
burst reaches `handle` as fewer messages, or in order. Package `holder` keeps the latest of a window (`NewLatest`),
reduces it (`NewReduce`), sorts it (`NewReorder`), sorts a jittery stream (`NewJitter`), or batches it (`NewBatch`):

```go
id, err := b.Subscribe(ctx, display, subscriber.WithHolder(holder.NewLatest[Reading](time.Second)))
```

`BroadcastBatch` hands a `message.Batch` to every subscriber in one send. A subscriber with a batch handler
(`subscriber.WithBatchHandler`) gets it whole, one without gets each value through `handle`:

```go
b.BroadcastBatch(ctx, &message.Slice[Reading]{north, south, east})

id, err := b.Subscribe(ctx, handle, subscriber.WithBatchHandler(func(ctx context.Context, _ uint64, batch message.Batch[Reading]) error {
 return uplink.Send(ctx, slices.Collect(batch.All()))
}))
```

A lost batch is reported once, with the batch in the error. Don't change a batch once passed.

## Dead letters and replay

`subscriber.WithDeadLetters` stores every message the subscriber loses, with its error. `subscriber.WithReplay` hands a
subscriber an iterator's values before any message, such as its dead letters from before a restart. `store.File` is a
queue on disk that does both, with a checksum per record so that a torn write is dropped on reopen:

```go
lost, err := store.OpenFile(dir, encode, decode, store.WithSyncInterval(time.Second), store.WithMaxSize(64<<20))
...
id, err := b.Subscribe(ctx, page, subscriber.WithReplay(lost.Replay(ctx)), subscriber.WithDeadLetters[Alert](lost))
...
b.Shutdown(ctx)
lost.Close()
```

To store and forward, subscribe `store.Enqueue(queue)` as `handle`, and let `store.Drain` hand each message to a slow
sink, in order, so that `Broadcast` never waits for it.

## Lifecycle

- `Unsubscribe` and `Close` never wait, so `handle` can call them. An unsubscribed subscriber still handles what it
  took, unless `subscriber.WithUnsubscribeDiscard`, which reports it as `*subscriber.ClosedError` instead.
- A subscriber is unsubscribed once its `Subscribe` ctx is done, unless `subscriber.WithDetachedContext`. That ctx is
  what `handle` and the error handlers get, unless `message.WithContext` replaces it for one message. The `Broadcast`
  ctx only bounds the wait, but async sends keep using it after `Broadcast` returns.
- `subscriber.WithEvict(evict)` unsubscribes a subscriber on the first error `evict` picks, such as a timeout.
- `subscriber.WithDone(ctx, done)` sends the subscriber's ID once it is done with what it took.
- `Shutdown(ctx)` is `Close`, then waits until every subscriber is done, or ctx is. Call it on SIGTERM.
- `Stats` returns each subscriber's counters: queued, handled, failed, lost, held, and how long `handle` has been
  running, for a watchdog.

A subscriber reconnects in place: `handle` or a middleware dials again, `middleware.Retry` retries, and the buffer
keeps what comes meanwhile in order ([`10-reconnect`](examples/10-reconnect/main.go)).

## Examples

[`examples/`](examples) holds runnable programs: `go run ./examples/01-basic`.

| Example | Shows |
| --- | --- |
| [`01-basic`](examples/01-basic/main.go) | `Subscribe`, `Broadcast`, and `handle` unsubscribing itself. |
| [`02-delivery`](examples/02-delivery/main.go) | Buffered, non-blocking, timed out and async delivery. |
| [`03-errors`](examples/03-errors/main.go) | Error handler, `middleware.Recover` and `middleware.WrapError`. |
| [`04-iterator`](examples/04-iterator/main.go) | `SubscribeSeq` and `fail`. |
| [`05-middleware`](examples/05-middleware/main.go) | `middleware.Retry` with backoff, and a middleware of your own. |
| [`06-filter`](examples/06-filter/main.go) | `subscriber.WithFilter` with `filter.Changed` and `filter.Every`. |
| [`07-holder`](examples/07-holder/main.go) | `holder.NewLatest` and `holder.Reduce` for bursts of readings. |
| [`08-batch`](examples/08-batch/main.go) | `BroadcastBatch`, `subscriber.WithBatchHandler` and `holder.NewBatch`. |
| [`09-file`](examples/09-file/main.go) | Dead letters on disk with `store.File`, replayed after a restart. |
| [`10-reconnect`](examples/10-reconnect/main.go) | A subscriber reconnecting in place, its buffer keeping order. |
| [`11-mqtt`](examples/11-mqtt/main.go) | MQTT fanned out to a rule, a local store, and an uplink that stores and forwards. |
| [`12-modbus`](examples/12-modbus/main.go) | A PLC polled over Modbus TCP, and a rule that retries and reconnects. |

`11-mqtt` and `12-modbus` are modules of their own, to keep their dependencies out of the library's go.mod.
`go run -C examples/11-mqtt .` needs an MQTT broker at `MQTT_ADDR` (`localhost:1883` by default). `12-modbus` simulates
its PLC in process.

## Performance

Time per message per subscriber, the median of 6 runs of `make bench` at `e28de1e` (Go 1.26.1, Intel Core Ultra 5
226V, 8 cores). Subscribers are buffered and waited for once, after the last `Broadcast`, unless "until handled".

| Benchmark | 1 subscriber | 10 | 100 | 1000 |
| --- | --- | --- | --- | --- |
| Sync | 223 ns | 152 ns | 220 ns | 341 ns |
| Sync, until handled | 652 ns | 697 ns | 685 ns | 482 ns |
| Sync, from 8 goroutines | 431 ns | 126 ns | 68 ns | 59 ns |
| Parallel | 1.44 µs | 1.46 µs | 858 ns | 722 ns |
| Async | 2.13 µs | 445 ns | 431 ns | 471 ns |
| Batch of 16, value by value | 94 ns | 34 ns | 19 ns | 19 ns |
| Batch of 16, to a batch handler | 15 ns | 10 ns | 15 ns | 22 ns |

A `Broadcast` without options allocates nothing in sync, buffered and non-blocking modes. Async allocates once per
subscriber (110 B), parallel twice (270 B). A `Subscribe` and `Unsubscribe` pair takes 1.2 µs and 12 allocations
(1.1 KiB).

## Development

```sh
make check     # golangci-lint, and go test -race -shuffle=on -cpu 1,4
make stress    # the stress test, 50 times
make benchcmp  # benchstat of main against the working tree
make tinygo    # with TinyGo: run each example and build it for a Raspberry Pi Pico
```

The library needs TinyGo 0.43 or later: 0.42 can deadlock a `Close` racing `Subscribe`s
([tinygo#5692](https://github.com/tinygo-org/tinygo/issues/5692)). Under TinyGo, `PanicError.Stack` is nil, and the
tests need Go, since they use `testing/synctest`.

## License

[MIT](LICENSE).
