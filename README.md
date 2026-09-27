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

id, err := b.Subscribe(ctx, func(ctx context.Context, msg string) error {
	fmt.Println("got", msg)
	return nil
}, broadcastor.WithSubscriberErrorHandler[string](func(ctx context.Context, err error) {
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
3. [`03-errors`](examples/03-errors/main.go): `WithSubscriberErrorHandler` and `*HandleError`.
4. [`04-recover`](examples/04-recover/main.go): `WithSubscriberRecover` and `*PanicError`.
5. [`05-unsubscribe`](examples/05-unsubscribe/main.go): a subscriber unsubscribing itself from `handle`.
6. [`06-buffer`](examples/06-buffer/main.go): `WithSubscriberBuffer`.
7. [`07-timeout`](examples/07-timeout/main.go): `WithMessageTimeout`, `WithMessageErrorHandler` and `*TimeoutError`.
8. [`08-async`](examples/08-async/main.go): `WithMessageAsync`.
9. [`09-non-blocking`](examples/09-non-blocking/main.go): `WithMessageNonBlocking` and `*DroppedError`.
10. [`10-defaults`](examples/10-defaults/main.go): `WithSubscriberDefaultMessageOptions`, overridden by `WithMessageSync`.
11. [`11-iterator`](examples/11-iterator/main.go): `SubscribeSeq` and a `for range` loop instead of `handle`.
12. [`12-broadcast-values`](examples/12-broadcast-values/main.go): `WithSubscriberBroadcastValues`, a trace ID from the
    `Broadcast` ctx reaching `handle`.

## Delivery

Delivery is at most once: a subscriber gets a message once, or misses it and never gets it later. Each `Broadcast` picks
a mode with a message option, and a subscriber can set its own default with `WithSubscriberDefaultMessageOptions`.

| Mode | Ordering per subscriber | What `Broadcast` waits for | When a subscriber misses a message |
| --- | --- | --- | --- |
| **Sync** (default, `WithMessageSync`) | In `Broadcast` order, for `Broadcast`s from one goroutine. | Each subscriber in turn, until it takes the message. A subscriber takes its next message only once `handle` returns, so a slow subscriber holds up `Broadcast` and every subscriber after it. | The `Broadcast` ctx is done, or the message's timeout runs out, before it takes the message (`*TimeoutError`). |
| **Buffered** (`WithSubscriberBuffer(n)`) | Same as sync. | Nothing while the subscriber's buffer has room, then the same as sync. | Same as sync, once the buffer is full. |
| **Async** (`WithMessageAsync`) | None: successive `Broadcast`s may arrive out of order. | Nothing. Each subscriber is sent the message from a goroutine of its own, so a slow subscriber holds up nobody else. | Same as sync, but reported after `Broadcast` has returned. |
| **Non-blocking** (`WithMessageNonBlocking`) | Same as sync, for the messages it takes. | Nothing. | It is busy in `handle`, or its buffer is full (`*DroppedError`). |

In every mode, a subscriber that is unsubscribed while `Broadcast` is running may miss the message
(`*SubscriberClosedError`).

`WithMessageTimeout(d)` bounds the wait for each subscriber separately, while a ctx deadline is used up across all of
them. `WithSubscriberTimeout(d)` sets a default timeout for every message sent to one subscriber.

## Context

`handle` gets the ctx passed to `Subscribe`, not the one passed to `Broadcast`, which may be done by the time the
subscriber takes the message. So by default, the values on the `Broadcast` ctx (a trace ID, a request-scoped logger)
never reach `handle`. With `WithSubscriberBroadcastValues`, they do: `handle` gets a ctx with the values of its
message's `Broadcast` ctx, then those of the `Subscribe` ctx, but with the deadline and cancellation of the `Subscribe`
ctx alone.

```go
id, err := b.Subscribe(ctx, func(ctx context.Context, msg string) error {
	log.Println(ctx.Value(traceIDKey{}), msg) // the trace ID of the request that broadcast msg
	return nil
}, broadcastor.WithSubscriberBroadcastValues[string]())
```

## Errors

Errors go to error handlers, and are discarded when there are none:

- `WithSubscriberErrorHandler` gets every error about its subscriber.
- `WithMessageErrorHandler` gets every error about its message, after the subscriber's handler. Every subscriber shares
  it, so it can be called concurrently, even after `Broadcast` has returned.

| Error | When | ctx given to the handler |
| --- | --- | --- |
| `*HandleError` | `handle` returned an error. | `handle`'s |
| `*PanicError` | `handle` panicked, in a subscriber with `WithSubscriberRecover`. Without it, the panic crashes the program. | `handle`'s |
| `*TimeoutError` | `Broadcast` gave up waiting. | `Broadcast`'s |
| `*DroppedError` | A non-blocking `Broadcast` found the subscriber busy. | `Broadcast`'s |
| `*SubscriberClosedError` | The subscriber was unsubscribed while `Broadcast` was running. | `Broadcast`'s |
| `*SubscriberClosedError` | A `SubscribeSeq` loop ended before yielding a message its subscriber took. | `SubscribeSeq`'s* |
| `*SubscriberClosedError` | A subscriber unsubscribed with `WithUnsubscribeDiscard` took a message. | `Subscribe`'s or `SubscribeSeq`'s* |

\* With `WithSubscriberBroadcastValues`, `handle`'s ctx and the ones marked with a star also have the values of the
message's `Broadcast` ctx.

Each error type matches a sentinel with `errors.Is` (`ErrTimeout`, `ErrDropped`, `ErrPanic`, `ErrSubscriberClosed`,
`ErrSubscriberNotFound`), without needing to know the message type.

## Unsubscribing

`Unsubscribe` never waits, so `handle` can unsubscribe its own subscriber. A subscriber may still get messages after
`Unsubscribe` returns: whatever is in its buffer, and the message of a `Broadcast` that was already sending to it. Its
goroutine ends once it has processed them. Cancelling the ctx passed to `Subscribe` cancels `handle`'s, but does not
unsubscribe the subscriber, unless it has `WithSubscriberAutoUnsubscribe`. That option unsubscribes it as soon as the
ctx passed to `Subscribe` or `SubscribeSeq` is done, even while `handle` or the loop body is running, or before the loop
starts.

A `SubscribeSeq` loop ends once its subscriber is unsubscribed and has yielded those messages. When it ends another way,
because it breaks or because the ctx passed to `SubscribeSeq` is done, it unsubscribes the subscriber itself. The
messages the subscriber took but did not yield are then reported as `*SubscriberClosedError`. The iterator can be ranged
over once.

`Close` unsubscribes every subscriber the same way, and from then on `Subscribe` and `SubscribeSeq` return `ErrClosed`,
so `Broadcast` reaches nobody. It never waits either, so `handle` can call it too, and it is safe to call concurrently
with anything, including another `Close`: the first returns nil, and every later one `ErrClosed`.

To stop a subscriber from processing what it takes after being unsubscribed, pass `WithUnsubscribeDiscard` to
`Unsubscribe`. Those messages are then reported as `*SubscriberClosedError` instead of being passed to `handle`, and a
`SubscribeSeq` loop ends right away. `WithSubscriberDefaultUnsubscribeOptions(WithUnsubscribeDiscard())` makes it the
subscriber's default, which is the only way `Close` applies it. `WithUnsubscribeDeliver` overrides that default for one
`Unsubscribe`.

## Development

```sh
make check  # golangci-lint + go test -race
make bench  # benchmarks
```

## License

[MIT](LICENSE)
