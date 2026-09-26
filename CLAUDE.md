# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

`github.com/elojah/broadcastor` is a single-package Go library (package `broadcastor`, no `cmd/`) that provides a generic in-process fan-out: one `Broadcast` call hands a message to every subscriber's `handle` function. Its only dependency is `github.com/google/uuid`. `go.mod` requires Go 1.26.1.

## Commands

```sh
go build ./...
go vet ./...
go test -race ./...                 # always use -race, the package is all concurrency
go test -race -run TestName ./...   # run a single test
make check                          # golangci-lint + tests
```

## Architecture

- `Broadcastor[T]` (`broadcastor.go`) keeps subscribers in a `sync.Map` from a `uuid.UUID` (v7) to a `*subscriber[T]` (`subscriber.go`), which holds a `chan message[T]` (**unbuffered** unless `WithSubscriberBuffer`) and an atomic reference count.
  - `Subscribe(ctx, handle, options...)` returns the subscriber's ID and starts its goroutine (`consume`), which calls `handle(ctx, msg)` for each message on the channel until it is closed. `ctx` is the one passed to `Subscribe`. Cancelling it does not end the subscription.
  - `Broadcast(ctx, msg, options...)` goes through the subscribers one at a time and sends `msg` to each one. A subscriber only reads its next message once `handle` returns, so a slow subscriber holds up delivery to all subscribers after it. Once `ctx` is done, `Broadcast` stops waiting: the current subscriber misses the message, and so may every later one (`select` picks at random between sending and giving up). A message's timeout (`WithMessageTimeout`, defaulting to `WithSubscriberTimeout`) also makes `Broadcast` stop waiting, but it is counted separately for each subscriber, from when `Broadcast` gets to it. `send` derives the timeout ctx itself and still passes the `Broadcast` ctx to the error handlers. With `WithMessageAsync`, `Broadcast` takes a reference on every subscriber (`acquire`) and returns right away, and each send runs in its own goroutine under the same `ctx`. A slow subscriber then holds up nobody else, but messages from successive async `Broadcast`s can reach a subscriber out of order.
- **Errors from `handle`**: if the subscriber was given `WithSubscriberErrorHandler`, each error is passed to the handler as a `*HandleError[T]` (subscriber ID, message, wrapped error), with the `Subscribe` ctx. The handler runs in the subscriber's goroutine right after `handle`, so a slow handler holds things up just like a slow `handle`. A message can also carry its own handler (`WithMessageErrorHandler`). Every error about that message then goes to **both** handlers, the subscriber's first (`subscriber.report`, the only place handlers are called; each one may be nil). A message handler is shared by every subscriber of a `Broadcast`, so it can be called concurrently and after `Broadcast` returns. When `Broadcast` can't hand a message over, it reports a `*TimeoutError[T]` (ctx done) or a `*SubscriberClosedError[T]` (unsubscribed since `Range` picked it up), with the `Broadcast` ctx. Without a handler, errors are discarded. They used to go to a channel returned by `Subscribe`. That was removed because an unread error blocked the subscriber, which then blocked `Broadcast` (see `TestSubscribe_ErrorsWithoutHandler`). Don't bring back an error path the library owns that blocks when nobody reads it. Blocking is only acceptable inside the user's own handler.
- **Closing by reference count**: the subscription itself holds one reference, and each `Broadcast` takes one (`acquire`) for as long as it sends to that subscriber. `Unsubscribe` removes the subscriber from the map and drops the subscription's reference (`release`). Whoever drops the count to 0 closes the channel. So the channel is closed right away if no `Broadcast` is sending to it, or else by the last such `Broadcast` once it is done. The subscriber's goroutine keeps processing until the channel is closed, so it can still get a message after `Unsubscribe` returns. Two rules keep this safe, and any change to how channels are closed or removed has to preserve them:
  - Only `release` closes a channel, and `acquire` refuses a count that is already 0, so a `Broadcast` that picked the subscriber up from `Range` just before it was unsubscribed skips it instead of sending on a closed channel.
  - Neither `Unsubscribe` nor `Broadcast` ever waits for another `Broadcast`. `handle` often calls `Unsubscribe` itself, and the subscriber is not reading at that point, so any such wait can deadlock (see `TestUnsubscribe_SelfDuringBroadcast` and `TestUnsubscribe_SeveralThenDrain`).
- Options (`options.go`): `SubscriberOption[T]` (`WithSubscriberBuffer`, `WithSubscriberErrorHandler`, `WithSubscriberDefaultMessageOptions`, `WithSubscriberTimeout`) is applied in `Subscribe`. `MessageOptions[T]` (`WithMessageAsync`, `WithMessageErrorHandler`, `WithMessageTimeout`) set up the `message[T]` (`message.go`) that a single `Broadcast` sends to one subscriber, which is synchronous by default. That message is built **per subscriber**: first a copy of `subscriber.defaults` (filled in once by `WithSubscriberDefaultMessageOptions`), then the `Broadcast`'s options, which override it. So a subscriber whose default is `WithMessageAsync` is never waited for, even by a sync `Broadcast`, and there is no option yet to turn async back off. `WithSubscriberTimeout(d)` is just shorthand for a default `WithMessageTimeout(d)`, so a `Broadcast`'s own timeout replaces it (0 means none). The `ctx` of `Unsubscribe` is unused.
- Errors are typed structs in `errors.go` (`*SubscriberNotFoundError`, `*HandleError[T]`, `*TimeoutError[T]`, `*SubscriberClosedError[T]`), not sentinel values.

## Tests

Most tests run inside a `synctest` bubble. There, a subscriber goroutine that never ends (because its channel was never closed, or because it is stuck) fails the test, and `synctest.Wait()` after `Unsubscribe` waits until the subscriber has processed everything. Every wait is bounded by `deadlockTimeout`, so a deadlock fails the test instead of hanging.
