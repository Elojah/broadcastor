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

See the [package documentation](https://pkg.go.dev/github.com/elojah/broadcastor) for runnable examples.

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

## Unsubscribing

`Unsubscribe` never waits, so `handle` can unsubscribe its own subscriber. A subscriber may still get messages after
`Unsubscribe` returns: whatever is in its buffer, and the message of a `Broadcast` that was already sending to it. Its
goroutine ends once it has processed them. `handle` gets the ctx passed to `Subscribe`, and cancelling that ctx does not
unsubscribe it.

## Development

```sh
make check  # golangci-lint + go test -race
make bench  # benchmarks
```

## License

[MIT](LICENSE)
