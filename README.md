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
| `*subscriber.ClosedError` | The subscriber was unsubscribed while `Broadcast` was running. |
| `*subscriber.ClosedError` | A `SubscribeSeq` loop ended before yielding a message its subscriber took. |
| `*subscriber.ClosedError` | A subscriber unsubscribed with `subscriber.WithUnsubscribeDiscard` took a message. |
| `*subscriber.StoreError` | Instead of any of the above, the subscriber's store failed to store the message. It still matches the error it replaces with `errors.Is` and `errors.As`. |

Both handlers get every error with the ctx the message is handled with: its own from `message.WithContext`, or else the
subscription's, never the `Broadcast` one (see [Contexts](#contexts)). Unless the message has its own, that ctx is done
only once the subscription is over, even when the error is `Broadcast` giving up because its own ctx is done.

Each error type matches a sentinel with `errors.Is` (`subscriber.ErrTimeout`, `subscriber.ErrDropped`,
`subscriber.ErrPanic`, `subscriber.ErrClosed`, `subscriber.ErrStore`, and `ErrSubscriberNotFound` for `Unsubscribe`),
without needing to know the message type.

## Storing lost messages

`subscriber.WithStore` gives a `subscriber.Store` every message the subscriber loses, as a `subscriber.Record`: the
subscriber's ID, the message, and the error about it, whatever it is in the table above. So every message a `Broadcast`
picks the subscriber up for is either handled or stored, once, and the store can be read later to handle the stored
ones again. Make `subscriber.WithUnsubscribeDiscard` the subscriber's default, and `Close` stores whatever it had not
handled yet:

```go
type deadLetters struct {
	mu      sync.Mutex
	records []subscriber.Record[string]
}

func (d *deadLetters) Put(_ context.Context, r subscriber.Record[string]) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, r)

	return nil
}

id, err := b.Subscribe(ctx, handle,
	subscriber.WithStore[string](&deadLetters{}),
	subscriber.WithDefaultUnsubscribeOptions[string](subscriber.WithUnsubscribeDiscard()),
)
```

`Put` is called right before the error handlers, from wherever they are, including from `Broadcast` for the messages it
could not hand over. So a slow `Put` holds things up like a slow error handler (and makes a non-blocking `Broadcast`
wait), and it may be called from several goroutines at once. Its ctx has the values of the ctx the message is handled
with, but is never done, since that ctx being done is often why the message was lost: `Put` must bound itself. When it
fails, the error handlers get a `*subscriber.StoreError` instead, and the message is not given to the store again.

## Unsubscribing

`Unsubscribe` never waits, so `handle` can unsubscribe its own subscriber. A subscriber may still get messages after
`Unsubscribe` returns: whatever is in its buffer, and the message of a `Broadcast` that was already sending to it. Its
goroutine ends once it has processed them. The ctx passed to `Subscribe` or `SubscribeSeq` is the subscription's: the
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
a `SubscribeSeq` loop ends right away. `subscriber.WithDefaultUnsubscribeOptions(subscriber.WithUnsubscribeDiscard())`
makes it the subscriber's default, which is the only way `Close` applies it. `subscriber.WithUnsubscribeDeliver`
overrides that default for one `Unsubscribe`.

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
make check  # golangci-lint + go test -race
make bench  # benchmarks
```

## License

[MIT](LICENSE)
