# broadcastor

[![CI](https://github.com/Elojah/broadcastor/actions/workflows/ci.yml/badge.svg)](https://github.com/Elojah/broadcastor/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/elojah/broadcastor.svg)](https://pkg.go.dev/github.com/elojah/broadcastor)

A generic in-process fan-out for Go: one `Broadcast` call hands a message to every subscriber. Each subscriber runs in
its own goroutine and handles its messages one at a time.

```sh
go get github.com/elojah/broadcastor
```

## Usage

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

n := b.Broadcast(ctx, "hello") // how many subscribers it was handed to

if err := b.Unsubscribe(ctx, id); err != nil {
 return err
}
```

`handle` is given the subscriber's ID, so it can unsubscribe itself with `b.Unsubscribe(ctx, id)`. The ctx passed to
`Subscribe` is the subscription's: once it is done, the subscriber is unsubscribed (see [Unsubscribing](#unsubscribing)).
[Contexts](#contexts) tells which ctx does what.

Options live next to what they configure: [`subscriber`](subscriber) holds those passed to `Subscribe`,
`SubscribeSeq` and `Unsubscribe`, along with `Handler`, `Middleware` and the errors about a subscriber's messages, and
[`message`](message) those passed to `Broadcast`.

`SubscribeSeq` returns an iterator instead of taking a `handle` function. The loop body takes the place of `handle`, and
the loop unsubscribes when it breaks or when `ctx` is done:

```go
_, seq, err := b.SubscribeSeq(ctx)
if err != nil {
 return err
}

for msg := range seq {
 fmt.Println("got", msg)
}
```

### Examples

[`examples/`](examples) holds small runnable programs, from simple to complex. Run one with
`go run ./examples/01-basic`.

1. [`01-basic`](examples/01-basic/main.go): one subscriber, `Subscribe`, `Broadcast`, `Unsubscribe`.
2. [`02-fanout`](examples/02-fanout/main.go): several subscribers, and what `Broadcast` returns.
3. [`03-errors`](examples/03-errors/main.go): `subscriber.WithErrorHandler`, `middleware.WrapError` and
   `*subscriber.HandleError`.
4. [`04-recover`](examples/04-recover/main.go): `middleware.Recover` and `*subscriber.PanicError`.
5. [`05-unsubscribe`](examples/05-unsubscribe/main.go): a subscriber unsubscribing itself from `handle`.
6. [`06-buffer`](examples/06-buffer/main.go): `subscriber.WithBuffer`.
7. [`07-timeout`](examples/07-timeout/main.go): `message.WithTimeout`, `message.WithErrorHandler` and
   `*subscriber.TimeoutError`.
8. [`08-async`](examples/08-async/main.go): `message.WithAsync`.
9. [`09-non-blocking`](examples/09-non-blocking/main.go): `message.WithNonBlocking` and `*subscriber.DroppedError`.
10. [`10-defaults`](examples/10-defaults/main.go): `subscriber.WithDefaultMessageOptions`, overridden by
    `message.WithSync`.
11. [`11-iterator`](examples/11-iterator/main.go): `SubscribeSeq` and a `for range` loop instead of `handle`.
12. [`12-middleware`](examples/12-middleware/main.go): `subscriber.WithMiddleware`, `middleware.Retry` with backoff, a
    logging middleware of its own and `middleware.WrapError`.
13. [`13-parallel`](examples/13-parallel/main.go): `message.WithParallel`, where a slow subscriber holds up nobody else
    but `Broadcast` still waits for it.
14. [`14-context`](examples/14-context/main.go): `message.WithContext`, carrying a request's values to `handle` and the
    error handlers, and an async `Broadcast` that outlives the request.
15. [`15-store`](examples/15-store/main.go): `subscriber.WithStore`, keeping the messages `handle` fails on and those
    `Close` discards.
16. [`16-store-and-forward`](examples/16-store-and-forward/main.go): `store.Enqueue` and `store.Drain`, forwarding every
    message in order to an uplink that is down for a while, without holding `Broadcast` up.
19. [`19-stats`](examples/19-stats/main.go): `Broadcastor.Stats`, for a subscriber stuck with a full buffer and one
    failing on every message.
20. [`20-evict`](examples/20-evict/main.go): `subscriber.WithEvictAfter` and `*subscriber.EvictedError`, for a stuck
    subscriber that `Broadcast` stops waiting for.
21. [`21-order`](examples/21-order/main.go): `subscriber.WithOrder` and `*subscriber.LateError`, handling readings by
    the time they were taken rather than the order they arrive in.
22. [`22-replay`](examples/22-replay/main.go): `broadcastor.WithHistory`, `store.History` and `subscriber.WithReplay`,
    for a subscriber that joins late and handles first the readings it missed.

## Delivery

Delivery is at most once: a subscriber gets a message once, or misses it and never gets it later, although
`subscriber.WithStore` keeps it so that it can be handled again (see [Storing lost messages](#storing-lost-messages)).
Each `Broadcast` picks a mode with a message option, and a subscriber can set its own default with
`subscriber.WithDefaultMessageOptions`.

| Mode | Ordering per subscriber | What `Broadcast` waits for | When a subscriber misses a message |
| --- | --- | --- | --- |
| **Sync** (default, `message.WithSync`) | In `Broadcast` order, for `Broadcast`s from one goroutine. | Each subscriber in turn, until it takes the message. A subscriber takes its next message only once `handle` returns, so a slow subscriber holds up `Broadcast` and every subscriber after it. | The `Broadcast` ctx is done, or the message's timeout runs out, before it takes the message (`*subscriber.TimeoutError`). |
| **Buffered** (`subscriber.WithBuffer(n)`) | Same as sync. | Nothing while the subscriber's buffer has room, then the same as sync. | Same as sync, once the buffer is full. |
| **Parallel** (`message.WithParallel`) | Same as sync. | Every subscriber at once, each from a goroutine of its own, until each takes the message or misses it. A slow subscriber holds up `Broadcast`, but nobody else. | Same as sync, reported before `Broadcast` returns. |
| **Async** (`message.WithAsync`) | None: successive `Broadcast`s may arrive out of order. | Nothing. Each subscriber is sent the message from a goroutine of its own, so a slow subscriber holds up nobody else. | Same as sync, but reported after `Broadcast` has returned: the `Broadcast` ctx still counts then (see [Contexts](#contexts)). |
| **Non-blocking** (`message.WithNonBlocking`) | Same as sync, for the messages it takes. | Nothing. | It is busy in `handle`, or its buffer is full (`*subscriber.DroppedError`). |

In every mode, a subscriber that is unsubscribed while `Broadcast` is running may miss the message
(`*subscriber.ClosedError`).

`message.WithTimeout(d)` bounds the wait for each subscriber separately, while a ctx deadline is used up across all of
them. A parallel `Broadcast` gets to every subscriber at once, so it waits at most `d` in all, and a ctx deadline gives
every subscriber the same time. `subscriber.WithTimeout(d)` sets a default timeout for every message sent to one
subscriber. `subscriber.WithEvictAfter(n)` unsubscribes a subscriber once it has lost n messages in a row, so that a
stuck one stops costing every `Broadcast` its timeout (see [Unsubscribing](#unsubscribing)).

## Ordering and replay

The ordering in the table above is the order a subscriber takes messages in. When messages carry their own order, such
as a timestamp or a sequence number, `subscriber.WithOrder` makes the subscriber handle them in that order instead,
whatever mode they were sent with and however many goroutines broadcast them:

```go
id, err := b.Subscribe(ctx, handle, subscriber.WithOrder(subscriber.OrderPolicy[Event]{
 Compare: subscriber.ByTime(func(e Event) time.Time { return e.At }), // or subscriber.By(func(e Event) uint64 { return e.Seq })
 Window:  50 * time.Millisecond,
 Limit:   1024,
}))
```

A live stream can never tell that nothing earlier is still on its way, so the policy bounds the wait:

- `Compare` orders the messages, as in `slices.SortFunc`. `subscriber.ByTime` and `subscriber.By` make one from a
  field.
- `Window` is the longest the subscriber holds a message, from when it takes it, for an earlier one to arrive. When a
  message's window ends, it is handled along with every message held that sorts before it, so none waits longer. With
  no window, the subscriber orders only the messages queued together in its buffer while `handle` was busy.
- `Limit` bounds how many messages it holds: past it, the first in order is handled early.

The subscriber keeps taking messages while it holds others, so holding them does not hold `Broadcast` up, but it takes
none while a message is due, so a slow `handle` still does. A message that should have come before one already handled
is late: it is reported as a `*subscriber.LateError`, and stored with `subscriber.WithStore`, instead of being handled
out of order. Equal messages keep the order they arrived in. Once unsubscribed, the subscriber handles what it holds
right away, in order, or reports it as a `*subscriber.ClosedError` with `subscriber.WithUnsubscribeDiscard`.
`WithOrder` applies to `SubscribeSeq` too.

`broadcastor.WithHistory(h)` makes `Broadcast` give every message to a `subscriber.History` before handing it to
anyone, and `subscriber.WithReplay(keep)` hands a new subscriber those of the history `keep` accepts, or all of them for
nil, before its live messages. `store.History` keeps the last ones in memory:

```go
b := broadcastor.NewBroadcastor(broadcastor.WithHistory(store.NewHistory[Event](1024)))
...
id, err := b.Subscribe(ctx, handle, subscriber.WithReplay(func(e Event) bool { return !e.At.Before(since) }))
```

Any type with these two methods is a history, so it can also keep messages in a database, across restarts:

```go
type History[T any] interface {
 Append(ctx context.Context, offset uint64, msg T)
 Read(ctx context.Context) ([]subscriber.HistoryEntry[T], error)
}
```

The library numbers the messages itself, from 1, and gives `Append` each one's offset, with the `Broadcast` ctx: a slow
`Append` holds `Broadcast` up, and concurrent `Broadcast`s call it at once, with offsets out of order. It cannot fail: a
message it does not keep is broadcast all the same, but never replayed. `Read` returns the messages kept, in any order.
If it fails, the subscriber's error handler gets a `*subscriber.ReplayError`, and the subscriber handles only live
messages. The library holds no lock around either.

The subscriber gets each message once, either replayed or live, with none missed in between, however `Subscribe` races
`Broadcast`, as long as `Read` returns every message `Append` was given and the history still keeps. `Subscribe` adds
the subscriber along with the newest offset, so that every `Broadcast` that finds it skips the older ones. It then
waits for the `Append`s of those still under way, and reads the history before returning. The replayed messages come
oldest first, or merged in order with the live ones with `WithOrder`. They get the subscriber's ctx and default message
options, not those their `Broadcast` was given, since it may be long over.

## Middleware

`subscriber.WithMiddleware` wraps `handle` in middlewares, the first one outermost. A `subscriber.Middleware[T]` takes
the next `subscriber.Handler[T]` and returns one that can act before or after calling it, change its error, call it
again (retry) or not call it at all (filter):

```go
logged := func(next subscriber.Handler[string]) subscriber.Handler[string] {
 return func(ctx context.Context, id uuid.UUID, msg string) error {
  err := next(ctx, id, msg)
  log.Println(id, msg, err)
  return err
 }
}

id, err := b.Subscribe(ctx, handle, subscriber.WithMiddleware(
 middleware.Recover[string](),
 middleware.WrapError[string](),
 logged,
))
```

Middlewares run in the subscriber's goroutine with the ctx `handle` gets, so a slow one holds the subscriber up like a
slow `handle`. Whatever error the outermost one returns reaches the error handlers as is. Middlewares have no
effect on `SubscribeSeq`, whose loop body runs in the caller's goroutine.

[`middleware`](middleware) holds ready-made ones:

- `middleware.Recover` makes a panic in the handler it wraps a `*subscriber.PanicError`, and the subscriber goes on
  with the next message. Without it, a panic in `handle` or a middleware crashes the program.
- `middleware.WrapError` makes an error a `*subscriber.HandleError`, with the subscriber and the message it failed on.
  Without it, error handlers get the error as `handle` returned it.
- `middleware.Retry(middleware.RetryPolicy{...})` calls `handle` again when it fails, and returns the error of the
  last call as is. The policy sets `Attempts` (the first call included), `Delay` before the first retry, a `Multiplier`
  for each wait after it, capped at `MaxDelay`, a `Jitter` between 0 and 1 that shortens each wait at random, and
  `IsRetryable` to skip the errors not worth retrying. It waits in the subscriber's goroutine, so the subscriber takes
  no message meanwhile, and a `Broadcast` waiting for it waits too. A done ctx ends the wait: the `Subscribe` ctx, which
  also unsubscribes the subscriber, or the message's own from `message.WithContext`. `Unsubscribe` alone does not.

```go
middleware.Retry[string](middleware.RetryPolicy{
 Attempts:    3,
 Delay:       10 * time.Millisecond,
 Multiplier:  2,
 IsRetryable: func(err error) bool { return !errors.Is(err, errInvalid) },
})
```

Give them first, in that order: `Recover` then also recovers panics in every later middleware, and its
`*subscriber.PanicError` is not wrapped in a `*subscriber.HandleError`. `Retry` then retries `handle`'s errors as is,
only the last one is wrapped, and panics are not retried.

## Errors

Errors go to error handlers, and are discarded when there are none:

- `subscriber.WithErrorHandler` gets every error about its subscriber.
- `message.WithErrorHandler` gets every error about its message, after the subscriber's handler. Every subscriber shares
  it, so it can be called concurrently, even after `Broadcast` has returned.

| Error | When |
| --- | --- |
| The error `handle` returned, as is | `handle`, or the outermost middleware, returned an error. |
| `*subscriber.HandleError` | Same, in a subscriber with `middleware.WrapError`. |
| `*subscriber.PanicError` | `handle` or a middleware panicked, in a subscriber with `middleware.Recover`. Without it, the panic crashes the program. |
| `*subscriber.TimeoutError` | `Broadcast` gave up waiting. |
| `*subscriber.DroppedError` | A non-blocking `Broadcast` found the subscriber busy. |
| `*subscriber.LateError` | A subscriber with `subscriber.WithOrder` took a message that should have come before one it already handled. |
| `*subscriber.ReplayError` | A subscriber with `subscriber.WithReplay` failed to read the history. It is about no message, so it reaches the subscriber's error handler only, and no store. |
| `*subscriber.ClosedError` | The subscriber was unsubscribed while `Broadcast` was running, or waiting on it. |
| `*subscriber.ClosedError` | A `SubscribeSeq` loop ended before yielding a message its subscriber took. |
| `*subscriber.ClosedError` | A subscriber unsubscribed with `subscriber.WithUnsubscribeDiscard` took a message. |
| `*subscriber.EvictedError` | Instead of the `*subscriber.TimeoutError` or `*subscriber.DroppedError` that evicted the subscriber (`subscriber.WithEvictAfter`). It still matches the error it replaces with `errors.Is` and `errors.As`. |
| `*subscriber.StoreError` | Instead of any of the above, the subscriber's store failed to store the message. It still matches the error it replaces with `errors.Is` and `errors.As`. |

Both handlers get every error with the ctx the message is handled with: its own from `message.WithContext`, or else the
subscription's, never the `Broadcast` one (see [Contexts](#contexts)). Unless the message has its own, that ctx is done
only once the subscription is over, even when the error is `Broadcast` giving up because its own ctx is done.

Each error type matches a sentinel with `errors.Is` (`subscriber.ErrTimeout`, `subscriber.ErrDropped`,
`subscriber.ErrPanic`, `subscriber.ErrClosed`, `subscriber.ErrStore`, `subscriber.ErrEvicted`, `subscriber.ErrLate`,
`subscriber.ErrReplay`, and
`ErrSubscriberNotFound` for `Unsubscribe`),
without needing to know the message type.

## Storing lost messages

`subscriber.WithStore` gives a `subscriber.Store` every message the subscriber loses, as a `subscriber.Record`: the
subscriber's ID, the message, and the error about it, whatever it is in the table above. So every message a `Broadcast`
picks the subscriber up for is either handled or stored, once, and the store can be read later to handle the stored
ones again. Make `subscriber.WithUnsubscribeDiscard` the subscriber's default, and `Close` stores whatever it had not
handled yet.

Package `store` holds ready-made stores. `store.Ring` keeps the records in memory, with room for a fixed number of
them: `Put` never waits, and once the ring is full it drops the oldest record and counts it (`Dropped`). It is a
`store.Queue`, which a single reader reads back oldest first: `Next` returns the oldest entry not yet acked, waiting
until there is one, and returns the same entry until `Ack` removes it.

```go
lost := store.NewRing[string](1024)
id, err := b.Subscribe(ctx, handle,
 subscriber.WithStore[string](lost),
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

Any type with a `Put(ctx, subscriber.Record[T]) error` method is a store, so it can also write to a database or a log.

`Put` is called right before the error handlers, from wherever they are, including from `Broadcast` for the messages it
could not hand over. So a slow `Put` holds things up like a slow error handler (and makes a non-blocking `Broadcast`
wait), and it may be called from several goroutines at once. Its ctx has the values of the ctx the message is handled
with, but is never done, since that ctx being done is often why the message was lost: `Put` must bound itself. When it
fails, the error handlers get a `*subscriber.StoreError` instead, and the message is not given to the store again.

`store.Drain` does that read-back loop: it hands each entry to a handle, in order, and acks it. An entry the handle fails
on goes to a dead-letter store, if one is given, and wrapping the handle in `middleware.Retry` retries it. With
`store.Enqueue` as the subscriber's handle, which only puts each message in the queue, that is store and forward:
`Broadcast` never waits for the sink, and the sink gets every message in order, even after being down for a while.

```go
queue := store.NewRing[string](1024)
id, err := b.Subscribe(ctx, store.Enqueue[string](queue))
...
retry := middleware.Retry[string](middleware.RetryPolicy{Attempts: math.MaxInt, Delay: time.Second, MaxDelay: time.Minute})
go store.Drain(ctx, queue, retry(uplink), nil)
```

`store.Filter` keeps some records out of a store, such as those that will never be handled.

## Stats

`Stats` returns a snapshot of each subscriber's counters since it subscribed, as a `subscriber.Stats`, in the order they
subscribed:

| Field | What |
| --- | --- |
| `Queued`, `Buffer` | Messages waiting in the subscriber's buffer, which has room for `Buffer` (`subscriber.WithBuffer`). |
| `Held` | Messages a subscriber with `subscriber.WithOrder` holds until they are due. |
| `Delivered` | Messages the subscriber took. |
| `Handled` | Messages `handle` returned nil for, or a `SubscribeSeq` loop body got. |
| `Failed` | Messages `handle`, or its outermost middleware, returned an error for, `*subscriber.PanicError` included. |
| `TimedOut`, `Dropped`, `Late` | Messages lost as a `*subscriber.TimeoutError`, a `*subscriber.DroppedError` or a `*subscriber.LateError`. |
| `HandleTime` | Time spent in `handle` and its middlewares (`middleware.Retry`'s waits included), or in the loop body, for `Handled + Failed` messages, so dividing gives the mean. |

Each message a `Broadcast` picks a subscriber up for is counted once in `Handled`, `Failed`, `TimedOut`, `Dropped` or
`Late`, or is still on its way: `Delivered` is `Handled + Failed + Late + Queued + Held`, plus the message in `handle`
if there is one. A replayed message counts as delivered once the subscriber takes it from the history. The counters are
read one at a time, so they may not add up while messages are in flight. A loss is counted before the error handlers
are called, so they see it counted.

```go
for _, s := range b.Stats() {
 log.Printf("%s: %d/%d queued, %d failed, %d lost", s.SubscriberID, s.Queued, s.Buffer, s.Failed, s.TimedOut+s.Dropped)
}
```

A subscriber leaves the snapshot once unsubscribed, even while it still processes what it took, so the messages it
misses as a `*subscriber.ClosedError` are never counted. `Stats` never waits, so `handle` and the error handlers can call
it. The counters are always on, and cost a few atomic adds and two clock reads per message.

## Unsubscribing

`Unsubscribe` never waits, so `handle` can unsubscribe its own subscriber. A `Broadcast` waiting on the subscriber gives
up then, and reports its message as a `*subscriber.ClosedError`. A subscriber may still get messages after
`Unsubscribe` returns: whatever is in its buffer, and the message of a `Broadcast` racing `Unsubscribe`. Its goroutine
ends once it has processed them. The ctx passed to `Subscribe` or `SubscribeSeq` is the subscription's: the
subscriber is unsubscribed as soon as it is done, even while `handle` or the loop body is running, or before the loop
starts. `handle` gets that ctx, so it gets it done for whatever the subscriber took before being unsubscribed. Pass
`subscriber.WithDetachedContext` to keep the subscriber subscribed until `Unsubscribe` or `Close` instead: `handle`, its
middlewares, its error handlers and the loop then get `context.WithoutCancel` of that ctx, with its values but never
done.

A `SubscribeSeq` loop ends once its subscriber is unsubscribed and has yielded those messages. When it ends another way,
because it breaks or because the ctx passed to `SubscribeSeq` is done, it unsubscribes the subscriber itself. The
messages the subscriber took but did not yield are then reported as `*subscriber.ClosedError`. The iterator can be
ranged over once.

`Close` unsubscribes every subscriber the same way, and from then on `Subscribe` and `SubscribeSeq` return `ErrClosed`,
so `Broadcast` reaches nobody. It never waits either, so `handle` can call it too, and it is safe to call concurrently
with anything, including another `Close`: the first returns nil, and every later one `ErrClosed`.

To stop a subscriber from processing what it takes after being unsubscribed, pass `subscriber.WithUnsubscribeDiscard`
to `Unsubscribe`. Those messages are then reported as `*subscriber.ClosedError` instead of being passed to `handle`, and
a `SubscribeSeq` loop ends right away. `subscriber.WithUnsubscribeOptions(subscriber.WithUnsubscribeDiscard())`
makes it the subscriber's default, which is the only way `Close` applies it. `subscriber.WithUnsubscribeDeliver`
overrides that default for one `Unsubscribe`.

A stuck subscriber with a timeout costs every `Broadcast` that timeout, for as long as it stays subscribed.
`subscriber.WithEvictAfter(n)` unsubscribes it once it has lost n messages in a row, as a `*subscriber.TimeoutError` or
a `*subscriber.DroppedError`, and a message it takes starts the count again. It is unsubscribed with
`subscriber.WithUnsubscribeDiscard`, whatever its defaults, so what is left in its buffer is reported rather than
handled, and a `Broadcast` waiting on it gives up. The loss that evicted it is reported once, as a
`*subscriber.EvictedError` wrapping the original error.

## Contexts

Each ctx has one job:

- The `Subscribe` or `SubscribeSeq` ctx is the subscription's lifetime, and what every message is handled with by
  default: `handle` and its middlewares get it, and so do the error handlers with every error about the subscriber's
  messages.
- The `Broadcast` ctx only bounds how long `Broadcast` waits for each subscriber to take the message. Its values reach
  neither `handle` nor the error handlers.
- `message.WithContext(ctx)` replaces the subscription's ctx for one message: `handle`, its middlewares and the error
  handlers get `ctx` as is, with its values (a trace or request ID) and its cancellation. Pass
  `context.WithoutCancel(ctx)` to keep only its values, since a buffered or async subscriber may take the message once
  `ctx` is done. The `Subscribe` ctx being done still unsubscribes the subscriber, but no longer reaches `handle` for
  that message.

An async send keeps using the `Broadcast` ctx after `Broadcast` has returned, and `Broadcast` has already counted it. So
the usual `defer cancel()` of a request makes every subscriber that has not taken the message by the time the request
returns miss it. To let the sends outlive the request, detach them and bound them with a timeout instead:

```go
detached := context.WithoutCancel(ctx)
b.Broadcast(detached, msg, message.WithAsync[string](), message.WithTimeout[string](time.Second), message.WithContext[string](detached))
```

This applies to a subscriber whose default is `message.WithAsync` too, even when the `Broadcast` does not ask for it.

## Development

```sh
make check     # golangci-lint + go test -race -shuffle=on -cpu 1,4, running each benchmark once
make bench     # benchmarks (BENCH=regexp)
make benchcmp  # benchstat of main against the working tree
make stress    # the stress test, 50 times
```

## License

[MIT](LICENSE)
