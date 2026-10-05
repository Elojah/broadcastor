<p align="center">
  <img src="assets/banner.svg" alt="One source sending the same message to every subscriber at once" width="360">
</p>

# broadcastor

[![CI](https://github.com/Elojah/broadcastor/actions/workflows/ci.yml/badge.svg)](https://github.com/Elojah/broadcastor/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/elojah/broadcastor.svg)](https://pkg.go.dev/github.com/elojah/broadcastor)

A generic in-process fan-out for Go: one `Broadcast` call hands a message to every subscriber. Each subscriber runs in
its own goroutine and handles its messages one at a time.

- **Delivery modes**: sync, buffered, parallel, async or non-blocking, with timeouts per message or per subscriber.
- **Filters**: a subscriber skips what it does not want before `Broadcast` wakes it up.
- **Typed errors**: every message a subscriber misses reaches its error handlers, and can be kept in a dead-letter
  store.
- **Middleware**: recover panics, keep a history, drop stale messages, wrap errors, retry with backoff, or write your
  own.
- **Operations**: evict stuck subscribers, bound async sends, and read each subscriber's counters at any time.
- One dependency: `github.com/google/uuid`.

```sh
go get github.com/elojah/broadcastor
```

## Quick start

```go
b := broadcastor.NewBroadcastor[string]()

id, err := b.Subscribe(ctx, func(ctx context.Context, id uuid.UUID, msg string) error {
 fmt.Println(id, "got", msg)
 return nil
}, subscriber.WithErrorHandler[string](func(ctx context.Context, err error) {
 log.Println(err)
}))
if err != nil {
 return err
}

n := b.Broadcast(ctx, "hello") // how many subscribers took it

if err := b.Unsubscribe(ctx, id); err != nil {
 return err
}
```

`handle` gets the subscriber's ID, so it can unsubscribe itself. The ctx given to `Subscribe` is the subscription's
lifetime (see [Lifecycle and contexts](#lifecycle-and-contexts)).

`SubscribeSeq` returns an iterator instead. The loop body takes the place of `handle`, and breaking out unsubscribes.
Each message comes with `fail`, which takes the error `handle` would return, so the middlewares, error handlers and
`Stats` treat the loop body as `handle`:

```go
_, seq, err := b.SubscribeSeq(ctx)
if err != nil {
 return err
}
for msg, fail := range seq {
 fail(save(msg)) // nil, or never calling fail, means handled
}
```

Options live in the package of what they configure:

| Package | Holds |
| --- | --- |
| [`broadcastor`](https://pkg.go.dev/github.com/elojah/broadcastor) | `Broadcastor`. |
| [`subscriber`](https://pkg.go.dev/github.com/elojah/broadcastor/subscriber) | The options for `Subscribe`, `SubscribeSeq` and `Unsubscribe`, the error types and `Stats`. |
| [`message`](https://pkg.go.dev/github.com/elojah/broadcastor/message) | The options for `Broadcast`. |
| [`middleware`](https://pkg.go.dev/github.com/elojah/broadcastor/middleware) | `Recover`, `History`, `MaxAge`, `WrapError` and `Retry`. |
| [`store`](https://pkg.go.dev/github.com/elojah/broadcastor/store) | Queues for dead letters and history (`Ring`, `Drain`, `Enqueue`, `Filter`). |
| [`filter`](https://pkg.go.dev/github.com/elojah/broadcastor/filter) | Filters for `subscriber.WithFilter` (`Changed`, `Every`). |

## Delivery

The mode is a message option, given to `Broadcast` or as a subscriber's default with
`subscriber.WithDefaultMessageOptions`. Buffering is a subscriber option.

| Mode | Option | `Broadcast` waits for | Order per subscriber |
| --- | --- | --- | --- |
| Sync (default) | `message.WithSync` | Each subscriber in turn, so a slow one holds up those after it. | Broadcast order¹ |
| Buffered | `subscriber.WithBuffer(n)` | Nothing until the subscriber's buffer is full, then as sync. | Broadcast order¹ |
| Parallel | `message.WithParallel` | Every subscriber at once. A slow one holds up `Broadcast`, but no other subscriber. | Broadcast order¹ |
| Async | `message.WithAsync` | Nothing: each send runs in a goroutine of its own. | None |
| Non-blocking | `message.WithNonBlocking` | Nothing: a busy subscriber misses the message. | Broadcast order¹ |

¹ For `Broadcast`s from one goroutine.

Each async send holds a goroutine until the subscriber takes the message. `subscriber.WithAsyncLimit(n)` drops an async
message once n sends are under way to the subscriber, and `Stats.Sending` shows how many are.

`subscriber.WithFilter(keep)` skips the messages `keep` rejects, in `Broadcast`'s goroutine before any send, so the
subscriber never wakes up for them. They are neither reported nor counted, in `Stats` or in what `Broadcast` returns.
Package `filter` holds filters for readings that repeat themselves, each with state of its own, so give each
subscriber its own:

```go
id, err := b.Subscribe(ctx, handle,
 // Only once the temperature moved by half a degree since the last one passed.
 subscriber.WithFilter(filter.Changed(func(prev, next float64) bool { return math.Abs(next-prev) >= 0.5 })),
)
id, err = b.Subscribe(ctx, uplink, subscriber.WithFilter(filter.Every[float64](time.Minute))) // at most one a minute
```

Delivery is at most once: a subscriber that misses a message never gets it later, but its error handlers learn why
(see [Errors](#errors)), and a [dead-letter store](#dead-letters) can keep it. `Broadcast` gives up on a subscriber once
its ctx is done or the message's timeout runs out. `message.WithTimeout(d)` gives each subscriber `d` of its own, while
a ctx deadline is shared by all of them. `subscriber.WithTimeout(d)` sets a subscriber's default.

## Errors

Errors go to error handlers, and are discarded when there are none:

- `subscriber.WithErrorHandler` gets every error about its subscriber's messages.
- `message.WithErrorHandler` gets every error about its message, after the subscriber's handler. All subscribers share
  it, so it may run concurrently, even after `Broadcast` has returned.

| Error | When |
| --- | --- |
| `handle`'s error, as is | `handle`, or its outermost middleware, failed. For `SubscribeSeq`, the error passed to `fail`. |
| `*subscriber.HandleError` | The same, wrapped by `middleware.WrapError`. |
| `*subscriber.PanicError` | `handle` panicked, and `middleware.Recover` recovered it. Without it, the program crashes. |
| `*subscriber.ExpiredError` | `middleware.MaxAge` found the message too old to hand to `handle`. |
| `*subscriber.TimeoutError` | `Broadcast` gave up waiting. |
| `*subscriber.DroppedError` | A non-blocking `Broadcast` found the subscriber busy, or an async one found `subscriber.WithAsyncLimit` sends under way. |
| `*subscriber.ClosedError` | The subscriber was unsubscribed before handling the message. |
| `*subscriber.EvictedError` | Wraps the loss that evicted the subscriber (`subscriber.WithEvictAfter`). |
| `*subscriber.StoreError` | Wraps a loss the dead-letter store failed to keep. |

Each type matches a sentinel with `errors.Is` (`subscriber.ErrTimeout`, `subscriber.ErrClosed`, …), without needing
the message type, and the wrappers still match what they wrap. The handlers get the ctx the message is handled with,
never the `Broadcast` one.

## Middleware

`subscriber.WithMiddleware` wraps `handle` in middlewares, the first one outermost. Package `middleware` holds
ready-made ones:

```go
logged := func(next subscriber.Handler[string]) subscriber.Handler[string] {
 return func(ctx context.Context, id uuid.UUID, msg string) error {
  err := next(ctx, id, msg)
  log.Println(id, msg, err)
  return err
 }
}

id, err := b.Subscribe(ctx, handle, subscriber.WithMiddleware(
 middleware.Recover[string](),        // a panic becomes a *subscriber.PanicError
 middleware.History[string](history), // each message handled is put in history
 middleware.WrapError[string](),      // an error becomes a *subscriber.HandleError
 middleware.Retry[string](middleware.RetryPolicy{Attempts: 3, Delay: 10 * time.Millisecond, Multiplier: 2}),
 logged,
))
```

Keep that order: `Recover` first also catches panics in every later middleware, `History` before `WrapError` and
`Retry` neither wraps nor retries a failed `Put`, and `Retry` after `WrapError` retries `handle`'s raw errors, so only
the last one is wrapped and panics are not retried.

`middleware.MaxAge(d, at)` does not hand `handle` a message older than `d`, such as a reading that waited behind a slow
`handle`, and returns a `*subscriber.ExpiredError` instead, so the message counts as `Failed` and reaches the dead
letters. `at` returns when the message was made, since the library stamps none. It goes between `History` and
`WrapError`, so that an expired message is neither recorded as handled, wrapped, nor retried. Middlewares run in the subscriber's goroutine, so a slow one, or
`Retry` waiting, holds the subscriber up like a slow `handle`.

With `SubscribeSeq`, they wrap the loop body, which runs in the caller's goroutine, so `Retry` yields a message again.
`Recover` cannot catch a panic in the loop body, which reaches the loop's caller.

## Dead letters

`subscriber.WithDeadLetters` gives a store every message the subscriber loses, with the error about it, so each message
is either handled or stored, once. `store.Ring` is an in-memory queue that a single reader reads back, oldest first:

```go
lost := store.NewRing[string](1024)
id, err := b.Subscribe(ctx, handle,
 subscriber.WithDeadLetters[string](lost),
 // So that Close stores what the subscriber has not handled yet.
 subscriber.WithUnsubscribeOptions[string](subscriber.WithUnsubscribeDiscard()),
)
...
for {
 entry, err := lost.Next(ctx) // entry.SubscriberID, entry.Message, entry.Err
 if err != nil {
  break // ctx is done
 }
 ...
 lost.Ack(ctx, entry.ID)
}
```

Any type with a `Put(ctx, subscriber.Record[T]) error` method is a store. `Put` runs where the error handlers do,
`Broadcast` included, and may run concurrently. Its ctx is never done, since a done ctx is often why the message was
lost, so `Put` must bound itself. When it fails, the error handlers get a `*subscriber.StoreError`. `store.Filter`
keeps some records out of a store.

`middleware.History` puts every message the subscriber handles in a store, with a nil `Err`. Given the same store,
`WithDeadLetters` puts the others, so the store gets each message once, handled or lost:

```go
history := store.NewRing[string](1024)
id, err := b.Subscribe(ctx, handle,
 subscriber.WithMiddleware(middleware.History[string](history)),
 subscriber.WithDeadLetters[string](history),
)
```

`History` puts once `handle` has returned, from the subscriber's goroutine, so a slow `Put` holds the subscriber up like
a slow `handle`. When `Put` fails, `History` returns its error. Losses are put from wherever they happen, `Broadcast`
included, so the store may get them out of order.

With `store.Enqueue` as the handle, the subscriber only queues each message, and `store.Drain` hands them to the real
sink in order, from one goroutine. That is store and forward: `Broadcast` never waits for the sink.

```go
queue := store.NewRing[string](1024)
id, err := b.Subscribe(ctx, store.Enqueue[string](queue))
...
retry := middleware.Retry[string](middleware.RetryPolicy{Attempts: math.MaxInt, Delay: time.Second, MaxDelay: time.Minute})
go store.Drain(ctx, queue, retry(uplink), nil)
```

[`19-redis`](examples/19-redis/stream.go) keeps such a queue in a Redis stream, which outlives a restart.

## Stats

`Stats` returns a snapshot of each subscriber's counters, in the order they subscribed. It never waits, so `handle` and
the error handlers can call it.

```go
for _, s := range b.Stats() {
 log.Printf("%s: %d/%d queued, %d failed, %d lost", s.SubscriberID, s.Queued, s.Buffer, s.Failed, s.TimedOut+s.Dropped)
}
```

| Field | What |
| --- | --- |
| `Queued`, `Buffer` | Messages in the subscriber's buffer, and its size. |
| `Sending` | Async sends under way to the subscriber. |
| `Delivered` | Messages the subscriber took. |
| `Handled`, `Failed` | Messages `handle` returned nil or an error for. |
| `TimedOut`, `Dropped` | Messages lost as a `TimeoutError` or a `DroppedError`. |
| `HandleTime` | Time spent in `handle` and its middlewares. |
| `Handling` | How long `handle` has been running on the current message, 0 when idle. |

Each message is counted once, in `Handled`, `Failed`, `TimedOut` or `Dropped`, before the error handlers run.
A subscriber leaves the snapshot once unsubscribed. The counters are always on, and cost a few atomic operations and
two clock reads per message.

A stuck `handle`, such as a hung serial read, shows in `Handling`, so a watchdog can unsubscribe its subscriber. That
does not end `handle`, but no `Broadcast` waits for the subscriber any more:

```go
for _, s := range b.Stats() {
 if s.Handling > time.Minute {
  b.Unsubscribe(ctx, s.SubscriberID, subscriber.WithUnsubscribeDiscard())
 }
}
```

## Lifecycle and contexts

- `Unsubscribe` and `Close` never wait, so `handle` can call them. A `Broadcast` waiting on the subscriber gives up,
  with a `*subscriber.ClosedError`. After `Close`, `Subscribe` and `SubscribeSeq` return `broadcastor.ErrClosed`.
- Once unsubscribed, a subscriber still handles what it already took: its buffer, and the message of a `Broadcast`
  racing `Unsubscribe`. With `subscriber.WithUnsubscribeDiscard`, it reports them as `*subscriber.ClosedError`
  instead. `subscriber.WithUnsubscribeOptions` makes that the subscriber's default, which is the only way `Close`
  applies it.
- `subscriber.WithEvictAfter(n)` unsubscribes a subscriber once it has lost n messages in a row, so that a stuck one
  stops costing every `Broadcast` its timeout.

Each ctx has one job:

| ctx | Role |
| --- | --- |
| `Subscribe`, `SubscribeSeq` | The subscription's lifetime: once it is done, the subscriber is unsubscribed, unless it has `subscriber.WithDetachedContext`. `handle`, its middlewares and the error handlers get it. |
| `Broadcast` | Only bounds how long `Broadcast` waits. Its values reach neither `handle` nor the error handlers. |
| `message.WithContext` | Replaces the `Subscribe` ctx for one message, values and cancellation included. |

Async sends keep using the `Broadcast` ctx after `Broadcast` has returned. So a request's `defer cancel()` drops the
message for every subscriber that has not taken it yet. To let the sends outlive the request, detach them and bound
them with a timeout instead:

```go
detached := context.WithoutCancel(ctx)
b.Broadcast(detached, msg, message.WithAsync[string](), message.WithTimeout[string](time.Second), message.WithContext[string](detached))
```

The same goes for a subscriber whose default is `message.WithAsync`.

## Examples

[`examples/`](examples) holds runnable programs, from simple to complex. Run one with `go run ./examples/01-basic`.

| Example | Shows |
| --- | --- |
| [`01-basic`](examples/01-basic/main.go) | `Subscribe`, `Broadcast` and `Unsubscribe`. |
| [`02-fanout`](examples/02-fanout/main.go) | Several subscribers, and what `Broadcast` returns. |
| [`03-errors`](examples/03-errors/main.go) | `subscriber.WithErrorHandler` and `middleware.WrapError`. |
| [`04-recover`](examples/04-recover/main.go) | `middleware.Recover` and `*subscriber.PanicError`. |
| [`05-unsubscribe`](examples/05-unsubscribe/main.go) | A subscriber unsubscribing itself from `handle`. |
| [`06-buffer`](examples/06-buffer/main.go) | `subscriber.WithBuffer`. |
| [`07-timeout`](examples/07-timeout/main.go) | `message.WithTimeout`, `message.WithErrorHandler` and `*subscriber.TimeoutError`. |
| [`08-async`](examples/08-async/main.go) | `message.WithAsync`. |
| [`09-non-blocking`](examples/09-non-blocking/main.go) | `message.WithNonBlocking` and `*subscriber.DroppedError`. |
| [`10-defaults`](examples/10-defaults/main.go) | `subscriber.WithDefaultMessageOptions`, overridden by `message.WithSync`. |
| [`11-iterator`](examples/11-iterator/main.go) | `SubscribeSeq`, a `for range` loop, and `fail`. |
| [`12-middleware`](examples/12-middleware/main.go) | `middleware.Retry` with backoff, and a middleware of your own. |
| [`13-parallel`](examples/13-parallel/main.go) | `message.WithParallel`, where a slow subscriber holds up nobody else. |
| [`14-context`](examples/14-context/main.go) | `message.WithContext`, and an async `Broadcast` that outlives a request. |
| [`15-dead-letters`](examples/15-dead-letters/main.go) | `subscriber.WithDeadLetters`, with the messages `Close` discards. |
| [`16-stats`](examples/16-stats/main.go) | `Broadcastor.Stats`, for a stuck subscriber and a failing one. |
| [`17-evict`](examples/17-evict/main.go) | `subscriber.WithEvictAfter` and `*subscriber.EvictedError`. |
| [`18-history`](examples/18-history/main.go) | `middleware.History` and `subscriber.WithDeadLetters` sharing a store. |
| [`19-redis`](examples/19-redis/main.go) | A dead-letter queue and a history in Redis, across a restart. |
| [`20-filter`](examples/20-filter/main.go) | `subscriber.WithFilter` with `filter.Changed` and `filter.Every`, for readings that repeat themselves. |
| [`21-max-age`](examples/21-max-age/main.go) | `middleware.MaxAge` and `*subscriber.ExpiredError`, for a reading that waited too long. |
| [`22-watchdog`](examples/22-watchdog/main.go) | `Stats.Handling` to unsubscribe a hung subscriber, and `subscriber.WithAsyncLimit`. |

`19-redis` is a module of its own, so that the library does not depend on go-redis. Run it with
`go run -C examples/19-redis .`, against the Redis at `REDIS_ADDR` (`localhost:6379` by default).

## Development

```sh
make check     # golangci-lint + go test -race -shuffle=on -cpu 1,4, running each benchmark once
make bench     # benchmarks (BENCH=regexp)
make benchcmp  # benchstat of main against the working tree
make stress    # the stress test, 50 times
```

## License

[MIT](LICENSE).
