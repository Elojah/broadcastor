<p align="center">
  <img src="assets/banner.svg" alt="One source sending the same message to every subscriber at once" width="360">
</p>

# broadcastor

[![CI](https://github.com/Elojah/broadcastor/actions/workflows/ci.yml/badge.svg)](https://github.com/Elojah/broadcastor/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/elojah/broadcastor.svg)](https://pkg.go.dev/github.com/elojah/broadcastor)

A generic in-process fan-out for Go: one `Broadcast` hands a message to every subscriber, and each subscriber handles
its messages one at a time, in its own goroutine.

- **Delivery modes**: sync, buffered, parallel, async or non-blocking, with timeouts and filters.
- **Typed errors**: every missed message reaches the error handlers, and can go to a dead-letter store.
- **Middleware**: recover, history, max age, error wrapping, retry with backoff, or your own.
- **Operations**: eviction, per-subscriber stats, graceful shutdown.
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

`handle` gets the subscriber's ID, so it can unsubscribe itself. `SubscribeSeq` returns an iterator instead, whose loop
body replaces `handle`. Breaking out unsubscribes:

```go
_, seq, err := b.SubscribeSeq(ctx)
if err != nil {
 return err
}
for msg, fail := range seq {
 fail(save(msg)) // nil, or never calling fail, means handled
}
```

| Package | Holds |
| --- | --- |
| [`broadcastor`](https://pkg.go.dev/github.com/elojah/broadcastor) | `Broadcastor`. |
| [`subscriber`](https://pkg.go.dev/github.com/elojah/broadcastor/subscriber) | Options for `Subscribe`, `SubscribeSeq` and `Unsubscribe`, error types, `Stats`. |
| [`message`](https://pkg.go.dev/github.com/elojah/broadcastor/message) | Options for `Broadcast`. |
| [`middleware`](https://pkg.go.dev/github.com/elojah/broadcastor/middleware) | `Recover`, `History`, `MaxAge`, `WrapError`, `Retry`. |
| [`store`](https://pkg.go.dev/github.com/elojah/broadcastor/store) | Queues for dead letters and history: `Ring`, `Drain`, `Enqueue`, `Filter`. |
| [`filter`](https://pkg.go.dev/github.com/elojah/broadcastor/filter) | Filters for `subscriber.WithFilter`: `Changed`, `Every`. |

## Delivery

The mode is a message option, passed to `Broadcast` or set as a subscriber's default with
`subscriber.WithDefaultMessageOptions`. Buffering is a subscriber option.

| Mode | Option | `Broadcast` waits for | Order per subscriber |
| --- | --- | --- | --- |
| Sync (default) | `message.WithSync` | Each subscriber in turn: a slow one holds up those after it. | Broadcast order¹ |
| Buffered | `subscriber.WithBuffer(n)` | Nothing until the buffer is full, then as sync. | Broadcast order¹ |
| Parallel | `message.WithParallel` | Every subscriber at once: a slow one holds up `Broadcast` only. | Broadcast order¹ |
| Async | `message.WithAsync` | Nothing: each send runs in its own goroutine. | None |
| Non-blocking | `message.WithNonBlocking` | Nothing: a busy subscriber misses the message. | Broadcast order¹ |

¹ For `Broadcast`s from one goroutine.

Delivery is at most once. `Broadcast` gives up on a subscriber once its ctx is done or the message's timeout runs out
(`message.WithTimeout`, or `subscriber.WithTimeout` by default), and the error handlers learn why. Each async send
holds a goroutine until taken, which `subscriber.WithAsyncLimit(n)` bounds.

`subscriber.WithFilter(keep)` skips what `keep` rejects before any send, so the subscriber never wakes up for it.
Package `filter` holds stateful filters, so give each subscriber its own:

```go
id, err := b.Subscribe(ctx, handle,
 // Once the temperature has moved by half a degree since the last one passed.
 subscriber.WithFilter(filter.Changed(func(prev, next float64) bool { return math.Abs(next-prev) >= 0.5 })),
)
id, err = b.Subscribe(ctx, uplink, subscriber.WithFilter(filter.Every[float64](time.Minute))) // at most one a minute
```

## Errors

Errors go to `subscriber.WithErrorHandler`, then to `message.WithErrorHandler`, which every subscriber shares, with the
ctx the message is handled with. Without either, they are discarded.

| Error | When |
| --- | --- |
| `handle`'s error, as is | `handle`, its outermost middleware, or `fail` for `SubscribeSeq`, failed. |
| `*subscriber.HandleError` | The same, wrapped by `middleware.WrapError`. |
| `*subscriber.PanicError` | `middleware.Recover` recovered a panic. Without it, the program crashes. |
| `*subscriber.ExpiredError` | `middleware.MaxAge` found the message too old. |
| `*subscriber.TimeoutError` | `Broadcast` gave up waiting. |
| `*subscriber.DroppedError` | A non-blocking `Broadcast` found the subscriber busy, or an async one hit `subscriber.WithAsyncLimit`. |
| `*subscriber.ClosedError` | The subscriber was unsubscribed before handling the message. |
| `*subscriber.EvictedError` | Wraps the error that evicted the subscriber (`subscriber.WithEvict`). |
| `*subscriber.StoreError` | Wraps a loss the dead-letter store failed to keep. |

Each type matches a sentinel with `errors.Is` (`subscriber.ErrTimeout`, `subscriber.ErrClosed`, …) without needing the
message type, and wrappers match what they wrap.

## Middleware

`subscriber.WithMiddleware` wraps `handle`, the first middleware outermost. Package `middleware` holds ready-made ones:

```go
logged := func(next subscriber.Handler[string]) subscriber.Handler[string] {
 return func(ctx context.Context, id uuid.UUID, msg string) error {
  err := next(ctx, id, msg)
  log.Println(id, msg, err)
  return err
 }
}

id, err := b.Subscribe(ctx, handle, subscriber.WithMiddleware(
 middleware.Recover[string](),                   // a panic becomes a *subscriber.PanicError
 middleware.History[string](history),            // each message handled is put in history
 middleware.MaxAge[string](time.Minute, sentAt), // an older message becomes a *subscriber.ExpiredError
 middleware.WrapError[string](),                 // an error becomes a *subscriber.HandleError
 middleware.Retry[string](middleware.RetryPolicy{Attempts: 3, Delay: 10 * time.Millisecond, Multiplier: 2}),
 logged,
))
```

Keep that order: `Recover` first catches panics in every later middleware, `History` records only handled messages,
and an expired message is neither wrapped nor retried. `Retry` after `WrapError` retries raw errors and wraps only the
last.

Middlewares run in the subscriber's goroutine, so `Retry` waiting holds it up like a slow `handle`. With
`SubscribeSeq` they wrap the loop body, but `Recover` cannot catch a panic in it.

## Dead letters

`subscriber.WithDeadLetters` stores every message the subscriber loses, with its error, so each message is handled or
stored, once. `store.Ring` is an in-memory queue, read back oldest first:

```go
lost := store.NewRing[string](1024)
id, err := b.Subscribe(ctx, handle,
 subscriber.WithDeadLetters[string](lost),
 // So that Close or Shutdown stores what the subscriber has not handled yet.
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

A store is anything with `Put(ctx, subscriber.Record[T]) error`. `Put` runs where the error handlers do, maybe
concurrently, with a ctx never done, so it must bound itself. `store.Filter` keeps some records out. Given the same
store, `middleware.History` and `WithDeadLetters` record every message once, handled or lost
([`18-history`](examples/18-history/main.go)).

With `store.Enqueue` as `handle`, the subscriber only queues, and `store.Drain` hands each message to a slow sink, in
order, so `Broadcast` never waits for the sink ([`24-mqtt`](examples/24-mqtt/main.go)):

```go
queue := store.NewRing[string](1024)
id, err := b.Subscribe(ctx, store.Enqueue[string](queue))
...
retry := middleware.Retry[string](middleware.RetryPolicy{Attempts: math.MaxInt, Delay: time.Second, MaxDelay: time.Minute})
go store.Drain(ctx, queue, retry(uplink), nil)
```

`subscriber.WithReplay` hands a subscriber an iterator's values before any message, such as its dead letters from
before a restart. `yield` returns once the value is handled or reported, so the iterator acks it then. A value lost
again goes back to the dead letters, so yield only what they held when the iterator started
([`19-redis`](examples/19-redis/stream.go)):

```go
func (s *stream[T]) Replay(ctx context.Context) iter.Seq[T] {
 return func(yield func(T) bool) {
  entries, _ := s.All(ctx) // what it holds now
  for _, entry := range entries {
   if !yield(entry.Message) {
    return
   }
   s.Ack(ctx, entry.ID)
  }
 }
}
...
id, err := b.Subscribe(ctx, page, subscriber.WithReplay(lost.Replay(ctx)), subscriber.WithDeadLetters[Alert](lost))
```

## Stats

`Stats` returns a snapshot of each subscriber's counters, in subscription order. It never waits, so `handle` can call
it.

```go
for _, s := range b.Stats() {
 log.Printf("%s: %d/%d queued, %d failed, %d lost", s.SubscriberID, s.Queued, s.Buffer, s.Failed, s.TimedOut+s.Dropped)
}
```

| Field | What |
| --- | --- |
| `Queued`, `Buffer` | Messages in the buffer, and its size. |
| `Sending` | Async sends under way. |
| `Delivered` | Messages taken, and values replayed. |
| `Handled`, `Failed` | Messages `handle` returned nil or an error for. |
| `TimedOut`, `Dropped` | Messages lost as a `TimeoutError` or a `DroppedError`. |
| `HandleTime` | Time spent in `handle` and its middlewares. |
| `Handling` | How long `handle` has been running on the current message, 0 when idle. |

Each message is counted once, in `Handled`, `Failed`, `TimedOut` or `Dropped`, before the error handlers run. An
unsubscribed subscriber leaves the snapshot. A watchdog can unsubscribe a subscriber whose `handle` hangs, from
`Handling` ([`22-watchdog`](examples/22-watchdog/main.go)).

## Lifecycle and contexts

- `Unsubscribe` and `Close` never wait, so `handle` can call them, and a `Broadcast` waiting on the subscriber gives
  up. After `Close`, `Subscribe` and `SubscribeSeq` return `broadcastor.ErrClosed`.
- An unsubscribed subscriber still handles what it took, unless `subscriber.WithUnsubscribeDiscard`, which reports it
  as `*subscriber.ClosedError` instead. `subscriber.WithUnsubscribeOptions` makes that the default, for `Close` too.
- `subscriber.WithEvict(evict, evicted)` unsubscribes a subscriber, discarding, on the first error `evict` picks: a
  timeout, so that a stuck one stops costing every `Broadcast` its timeout, or an error from `handle` meaning it cannot
  go on. To evict after several errors, count them in a middleware ([`17-evict`](examples/17-evict/main.go)).
- `subscriber.WithDone(done)` sends the subscriber's ID once it is done with what it took, so the receiver can release
  what `handle` used without a lock.
- Like `signal.Notify`, `evicted` and `done` are sent without waiting: give the channel room for one per subscriber
  sharing it.
- `Shutdown(ctx)` is `Close`, then waits until every subscriber is done, or ctx is. Call it on SIGTERM: exiting right
  after `Close` cuts `handle` off.

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
defer stop()
<-ctx.Done()

ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
if err := b.Shutdown(ctx); err != nil {
 log.Println(err) // context.DeadlineExceeded: a handle is still running
}
```

| ctx | Role |
| --- | --- |
| `Subscribe`, `SubscribeSeq` | The subscription's lifetime, unless `subscriber.WithDetachedContext`. `handle`, its middlewares and the error handlers get it. |
| `Broadcast` | Only bounds the wait. Its values reach neither `handle` nor the error handlers. |
| `message.WithContext` | Replaces the `Subscribe` ctx for one message, values and cancellation included. |

Async sends keep using the `Broadcast` ctx after `Broadcast` returns, so a request's `defer cancel()` drops the message
for every subscriber yet to take it. To outlive the request, detach the sends and bound them with a timeout:

```go
detached := context.WithoutCancel(ctx)
b.Broadcast(detached, msg, message.WithAsync[string](), message.WithTimeout[string](time.Second), message.WithContext[string](detached))
```

## Reconnecting

A subscriber reconnects in place: `handle` owns the connection, closes it when a call fails, and dials again on the
next attempt, which `middleware.Retry` makes. What comes meanwhile waits in the buffer, in order. Once
`subscriber.WithDone` sends the ID, close the last connection: receiving it orders the close after `handle`'s last
call, so none of it needs a lock.

```go
var conn *Conn // only handle uses it, until done gets the ID
handle := func(ctx context.Context, _ uuid.UUID, msg Reading) error {
 if conn == nil {
  c, err := dial(ctx)
  if err != nil {
   return err
  }
  conn = c
 }
 if err := conn.Send(msg); err != nil {
  conn.Close()
  conn = nil // dial again on the next attempt
  return err
 }
 return nil
}
done := make(chan uuid.UUID, 1)
id, err := b.Subscribe(ctx, handle,
 subscriber.WithMiddleware(middleware.Retry[Reading](middleware.RetryPolicy{
  Attempts: math.MaxInt, Delay: time.Second, Multiplier: 2, MaxDelay: time.Minute,
 })),
 subscriber.WithBuffer[Reading](64),           // what comes in meanwhile, in order
 subscriber.WithTimeout[Reading](time.Second), // then Broadcast stops waiting
 subscriber.WithDone[Reading](done),
)
// ...
b.Shutdown(ctx)
<-done
if conn != nil {
 conn.Close()
}
```

For an outage longer than a buffer holds, store and forward with `store.Enqueue` and `store.Drain`. A call that hangs
never fails, so give it a timeout. `Unsubscribe` does not end a `Retry` wait: the subscription's ctx does. See
[`26-reconnect`](examples/26-reconnect/main.go) and [`25-modbus`](examples/25-modbus/main.go).

## Examples

[`examples/`](examples) holds runnable programs, from simple to complex: `go run ./examples/01-basic`.

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
| [`13-parallel`](examples/13-parallel/main.go) | `message.WithParallel`: a slow subscriber holds up nobody else. |
| [`14-context`](examples/14-context/main.go) | `message.WithContext`, and an async `Broadcast` outliving a request. |
| [`15-dead-letters`](examples/15-dead-letters/main.go) | `subscriber.WithDeadLetters`, with what `Close` discards. |
| [`16-stats`](examples/16-stats/main.go) | `Broadcastor.Stats`, for a stuck subscriber and a failing one. |
| [`17-evict`](examples/17-evict/main.go) | `subscriber.WithEvict` on a timeout and on failures counted by a middleware. |
| [`18-history`](examples/18-history/main.go) | `middleware.History` and `subscriber.WithDeadLetters` sharing a store. |
| [`19-redis`](examples/19-redis/main.go) | Dead letters and history in Redis, replayed after a restart with `subscriber.WithReplay`. |
| [`20-filter`](examples/20-filter/main.go) | `subscriber.WithFilter` with `filter.Changed` and `filter.Every`. |
| [`21-max-age`](examples/21-max-age/main.go) | `middleware.MaxAge` and `*subscriber.ExpiredError`. |
| [`22-watchdog`](examples/22-watchdog/main.go) | `Stats.Handling` to unsubscribe a hung subscriber, and `subscriber.WithAsyncLimit`. |
| [`23-shutdown`](examples/23-shutdown/main.go) | `Shutdown` waiting for a slow `handle` and the dead letters. |
| [`24-mqtt`](examples/24-mqtt/main.go) | MQTT fanned out to a rule, a local store, and an uplink that stores and forwards. |
| [`25-modbus`](examples/25-modbus/main.go) | A PLC polled over Modbus TCP, filtered, and a rule that retries and reconnects in place. |
| [`26-reconnect`](examples/26-reconnect/main.go) | A subscriber reconnecting in place, its buffer keeping order. |

`19-redis`, `24-mqtt` and `25-modbus` are modules of their own, to keep their dependencies out of the library's go.mod.
`go run -C examples/19-redis .` needs Redis at `REDIS_ADDR` (`localhost:6379` by default), and `24-mqtt` an MQTT broker
at `MQTT_ADDR` (`localhost:1883` by default). `25-modbus` simulates its PLC in process.

## Development

```sh
make check     # golangci-lint + go test -race -shuffle=on -cpu 1,4, running each benchmark once
make bench     # benchmarks (BENCH=regexp)
make benchcmp  # benchstat of main against the working tree
make stress    # the stress test, 50 times
make tinygo    # with TinyGo: pkg/gate's tests, each example run and checked, and built for a Raspberry Pi Pico
```

## TinyGo

The library works with TinyGo 0.43, a dev build until released. `make tinygo` runs each example on Linux, checks its
output, and builds it for a Raspberry Pi Pico, about 150 KB of code. Nothing runs on the Pico, so whether a goroutine
per subscriber fits a microcontroller's RAM is up to the program. Under TinyGo:

- 0.42 and earlier can deadlock a `Close` racing `Subscribe`s
  ([tinygo#5692](https://github.com/tinygo-org/tinygo/issues/5692)).
- `PanicError.Stack` is nil.
- The tests need Go: most use `testing/synctest`, which TinyGo lacks, like the race detector.

## License

[MIT](LICENSE).
