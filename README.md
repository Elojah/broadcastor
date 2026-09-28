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

`handle` is given the subscriber's ID, so it can unsubscribe itself with `b.Unsubscribe(ctx, id)`.

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
12. [`12-middleware`](examples/12-middleware/main.go): `subscriber.WithMiddleware`, a retry middleware and
    `middleware.WrapError`.
13. [`13-parallel`](examples/13-parallel/main.go): `message.WithParallel`, where a slow subscriber holds up nobody else
    but `Broadcast` still waits for it.

## Delivery

Delivery is at most once: a subscriber gets a message once, or misses it and never gets it later. Each `Broadcast` picks
a mode with a message option, and a subscriber can set its own default with `subscriber.WithDefaultMessageOptions`.

| Mode | Ordering per subscriber | What `Broadcast` waits for | When a subscriber misses a message |
| --- | --- | --- | --- |
| **Sync** (default, `message.WithSync`) | In `Broadcast` order, for `Broadcast`s from one goroutine. | Each subscriber in turn, until it takes the message. A subscriber takes its next message only once `handle` returns, so a slow subscriber holds up `Broadcast` and every subscriber after it. | The `Broadcast` ctx is done, or the message's timeout runs out, before it takes the message (`*subscriber.TimeoutError`). |
| **Buffered** (`subscriber.WithBuffer(n)`) | Same as sync. | Nothing while the subscriber's buffer has room, then the same as sync. | Same as sync, once the buffer is full. |
| **Parallel** (`message.WithParallel`) | Same as sync. | Every subscriber at once, each from a goroutine of its own, until each takes the message or misses it. A slow subscriber holds up `Broadcast`, but nobody else. | Same as sync, reported before `Broadcast` returns. |
| **Async** (`message.WithAsync`) | None: successive `Broadcast`s may arrive out of order. | Nothing. Each subscriber is sent the message from a goroutine of its own, so a slow subscriber holds up nobody else. | Same as sync, but reported after `Broadcast` has returned. |
| **Non-blocking** (`message.WithNonBlocking`) | Same as sync, for the messages it takes. | Nothing. | It is busy in `handle`, or its buffer is full (`*subscriber.DroppedError`). |

In every mode, a subscriber that is unsubscribed while `Broadcast` is running may miss the message
(`*subscriber.ClosedError`).

`message.WithTimeout(d)` bounds the wait for each subscriber separately, while a ctx deadline is used up across all of
them. A parallel `Broadcast` gets to every subscriber at once, so it waits at most `d` in all, and a ctx deadline gives
every subscriber the same time. `subscriber.WithTimeout(d)` sets a default timeout for every message sent to one
subscriber.

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

Middlewares run in the subscriber's goroutine with the ctx passed to `Subscribe`, so a slow one holds the subscriber up
like a slow `handle`. Whatever error the outermost one returns reaches the error handlers as is. Middlewares have no
effect on `SubscribeSeq`, whose loop body runs in the caller's goroutine.

[`middleware`](middleware) holds ready-made ones:

- `middleware.Recover` makes a panic in the handler it wraps a `*subscriber.PanicError`, and the subscriber goes on
  with the next message. Without it, a panic in `handle` or a middleware crashes the program.
- `middleware.WrapError` makes an error a `*subscriber.HandleError`, with the subscriber and the message it failed on.
  Without it, error handlers get the error as `handle` returned it.

Give them first, in that order: `Recover` then also recovers panics in every later middleware, and its
`*subscriber.PanicError` is not wrapped in a `*subscriber.HandleError`.

## Errors

Errors go to error handlers, and are discarded when there are none:

- `subscriber.WithErrorHandler` gets every error about its subscriber.
- `message.WithErrorHandler` gets every error about its message, after the subscriber's handler. Every subscriber shares
  it, so it can be called concurrently, even after `Broadcast` has returned.

| Error | When | ctx given to the handler |
| --- | --- | --- |
| The error `handle` returned, as is | `handle`, or the outermost middleware, returned an error. | `Subscribe`'s |
| `*subscriber.HandleError` | Same, in a subscriber with `middleware.WrapError`. | `Subscribe`'s |
| `*subscriber.PanicError` | `handle` or a middleware panicked, in a subscriber with `middleware.Recover`. Without it, the panic crashes the program. | `Subscribe`'s |
| `*subscriber.TimeoutError` | `Broadcast` gave up waiting. | `Broadcast`'s |
| `*subscriber.DroppedError` | A non-blocking `Broadcast` found the subscriber busy. | `Broadcast`'s |
| `*subscriber.ClosedError` | The subscriber was unsubscribed while `Broadcast` was running. | `Broadcast`'s |
| `*subscriber.ClosedError` | A `SubscribeSeq` loop ended before yielding a message its subscriber took. | `SubscribeSeq`'s |
| `*subscriber.ClosedError` | A subscriber unsubscribed with `subscriber.WithUnsubscribeDiscard` took a message. | `Subscribe`'s or `SubscribeSeq`'s |

Each error type matches a sentinel with `errors.Is` (`subscriber.ErrTimeout`, `subscriber.ErrDropped`,
`subscriber.ErrPanic`, `subscriber.ErrClosed`, and `ErrSubscriberNotFound` for `Unsubscribe`), without needing to know
the message type.

## Unsubscribing

`Unsubscribe` never waits, so `handle` can unsubscribe its own subscriber. A subscriber may still get messages after
`Unsubscribe` returns: whatever is in its buffer, and the message of a `Broadcast` that was already sending to it. Its
goroutine ends once it has processed them. `handle` gets the ctx passed to `Subscribe`, and cancelling that ctx does not
unsubscribe it, unless the subscriber has `subscriber.WithAutoUnsubscribe`. That option unsubscribes it as soon as the
ctx passed to `Subscribe` or `SubscribeSeq` is done, even while `handle` or the loop body is running, or before the loop
starts.

A `SubscribeSeq` loop ends once its subscriber is unsubscribed and has yielded those messages. When it ends another way,
because it breaks or because the ctx passed to `SubscribeSeq` is done, it unsubscribes the subscriber itself. The
messages the subscriber took but did not yield are then reported as `*subscriber.ClosedError`. The iterator can be
ranged over once.

`Close` unsubscribes every subscriber the same way, and from then on `Subscribe` and `SubscribeSeq` return `ErrClosed`,
so `Broadcast` reaches nobody. It never waits either, so `handle` can call it too, and it is safe to call concurrently
with anything, including another `Close`: the first returns nil, and every later one `ErrClosed`.

To stop a subscriber from processing what it takes after being unsubscribed, pass `subscriber.WithUnsubscribeDiscard`
to `Unsubscribe`. Those messages are then reported as `*subscriber.ClosedError` instead of being passed to `handle`, and
a `SubscribeSeq` loop ends right away. `subscriber.WithDefaultUnsubscribeOptions(subscriber.WithUnsubscribeDiscard())`
makes it the subscriber's default, which is the only way `Close` applies it. `subscriber.WithUnsubscribeDeliver`
overrides that default for one `Unsubscribe`.

## Development

```sh
make check  # golangci-lint + go test -race
make bench  # benchmarks
```

## License

[MIT](LICENSE)
