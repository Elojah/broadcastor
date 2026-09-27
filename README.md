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

n, err := b.Broadcast(ctx, "hello") // how many subscribers it was handed to
if err != nil {
	return err // ErrClosed, once Close has been called
}

if err := b.Unsubscribe(ctx, id); err != nil {
	return err
}
```

Or hand the messages to a range loop instead of a `handle` function. The loop body runs in the caller's goroutine, and
the subscription ends with the loop:

```go
_, msgs, err := b.SubscribeSeq(ctx)
if err != nil {
	return err
}
for msg := range msgs { // ends once ctx is done, or on Unsubscribe or Close
	fmt.Println("got", msg)
	if msg == "stop" {
		break // unsubscribes
	}
}
```

When you are done, `Close` unsubscribes everyone and waits until they have handled what they were sent:

```go
if err := b.Close(ctx); err != nil {
	return err // ctx was done first
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
11. [`11-seq`](examples/11-seq/main.go): `SubscribeSeq`, a range loop instead of `handle`.
12. [`12-close`](examples/12-close/main.go): `Close` and `ErrClosed`.

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

## Errors

Errors go to error handlers, and are discarded when there are none:

- `WithSubscriberErrorHandler` gets every error about its subscriber.
- `WithMessageErrorHandler` gets every error about its message, after the subscriber's handler. Every subscriber shares
  it, so it can be called concurrently, even after `Broadcast` has returned.

| Error | When | ctx given to the handler |
| --- | --- | --- |
| `*HandleError` | `handle` returned an error. | `Subscribe`'s |
| `*PanicError` | `handle` panicked, in a subscriber with `WithSubscriberRecover`. Without it, the panic crashes the program. | `Subscribe`'s |
| `*TimeoutError` | `Broadcast` gave up waiting. | `Broadcast`'s |
| `*DroppedError` | A non-blocking `Broadcast` found the subscriber busy. | `Broadcast`'s |
| `*SubscriberClosedError` | The subscriber was unsubscribed while `Broadcast` was running. | `Broadcast`'s |

Each error type matches a sentinel with `errors.Is` (`ErrTimeout`, `ErrDropped`, `ErrPanic`, `ErrSubscriberClosed`,
`ErrSubscriberNotFound`), without needing to know the message type.

Errors about a call itself are returned instead: `ErrClosed` from `Subscribe`, `SubscribeSeq`, `Broadcast` and `Close`
once `Close` has been called, `*SubscriberNotFoundError` from `Unsubscribe`, and ctx's error from `Close` when it gives
up waiting.

## Unsubscribing

`Unsubscribe` never waits, so `handle` can unsubscribe its own subscriber. A subscriber may still get messages after
`Unsubscribe` returns: whatever is in its buffer, and the message of a `Broadcast` that was already sending to it. Its
goroutine ends once it has processed them. `handle` gets a ctx derived from the one passed to `Subscribe`, and
cancelling that ctx does not unsubscribe it.

A range loop over `SubscribeSeq` unsubscribes when the loop body breaks out of it, returns or panics, or when the ctx
passed to `SubscribeSeq` is done. The messages it had taken but not yielded yet are dropped. `Unsubscribe` and `Close`
end the loop too, but only once it has yielded those messages.

## Closing

`Close(ctx)` unsubscribes every subscriber, then waits until each one's goroutine has processed what it was sent and
ended, or until ctx is done. From then on, `Subscribe`, `SubscribeSeq`, `Broadcast` and `Close` fail with `ErrClosed`.
It only waits for the subscribers it unsubscribes, not for those unsubscribed before, which may still be draining.

`Close` does not wait for range loops over `SubscribeSeq`, which run in your goroutines: each ends once it has yielded
what its subscriber had already taken. It cannot wait for the goroutine it is called from either, so from `handle` (or
an error handler called with a `*HandleError` or a `*PanicError`), pass it the ctx that `handle` was given: `Close` then
waits for every subscriber but that one. With any other ctx, it waits for `handle` to return, which it cannot do before
`Close` returns, until that ctx is done.

## Development

```sh
make check  # golangci-lint + go test -race
make bench  # benchmarks
```

## License

[MIT](LICENSE)
