# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository. Behaviour is documented in godoc and the README. This file holds only the commands, the invariants that are easy to break, and decisions not to revert.

## Overview

`github.com/elojah/broadcastor` is a Go library: a generic in-process fan-out, where one `Broadcast` hands a message to every subscriber. Its only dependency is `github.com/google/uuid`. Go 1.26.1.

- `broadcastor` (root): `Broadcastor`, `ErrClosed`, `SubscriberNotFoundError`.
- `subscriber`: `Subscriber`, the options for `Subscribe`/`SubscribeSeq`/`Unsubscribe`, `Handler`/`Middleware`, `Store`/`Record`, and the errors about messages.
- `message`: `Message`, `Config`, `Delivery`, the options for `Broadcast`.
- `middleware` (`Recover`, `WrapError`, `Retry`), `store` (`Queue`, `Ring`, `Drain`, `Enqueue`, `Filter`), `pkg/gate`, `examples/`.

Imports go one way: `broadcastor` → `subscriber` → `message`. `middleware` and `store` import `subscriber`, never `broadcastor`. Planned work is in TODO.md.

## Commands

```sh
go test -race ./...                 # always -race: the package is all concurrency
go test -race -run TestName ./...
make check                          # golangci-lint + tests
make bench
```

Lint (`.golangci.yml`) is `default: all` minus a disable list, tests included: `nlreturn`, `paralleltest` (`t.Parallel()` first), `forcetypeassert`, `err113`, `godot`, `funcorder` (exported methods first). CI installs a pinned golangci-lint into `./bin` and runs `make check`.

## Conventions

- Comments are short: invariants, non-obvious whys, and the public contract. Don't restate the code, and state each fact once.
- Options have no `WithSubscriber`/`WithMessage` prefix, since the package name says it: `subscriber.WithBuffer`, `message.WithAsync`.
- `Subscriber` exports only what `Broadcastor` calls. The reference counting, `report`, `pull` and `discard` stay unexported, so the invariants below hold whatever a caller does.
- Errors are typed structs with a `SubscriberID`. Each type the library creates has an `Is` matching one sentinel, so `errors.Is` works without `T`. `broadcastor.ErrClosed` and `subscriber.ErrClosed` are different sentinels.

## Invariants

Any change to channels, removal or error reporting must keep these.

- **Only `release` closes a channel.** The subscription holds one reference, and each `Broadcast` sending to a subscriber holds another (`acquire` in `Deliver`). `acquire` refuses a count of 0, so a `Broadcast` that picked a subscriber up just before its removal skips it (`ClosedError`) instead of sending on a closed channel.
- **Nothing waits for a `Broadcast`**: not `Unsubscribe`, `Close`, the ctx-lifetime removal, nor another `Broadcast`. `handle` often unsubscribes while a `Broadcast` waits on it, and the subscriber is not reading then (`TestUnsubscribe_SelfDuringBroadcast`, `TestUnsubscribe_SeveralThenDrain`, `TestClose_FromHandle`).
- **Every removal goes through `remove`** (`LoadAndDelete` + `Subscriber.Unsubscribe`), so racing `Unsubscribe`, `Close` and ctx-done drop the reference and apply the unsubscribe options once. `Subscriber.Unsubscribe` stops the ctx-lifetime `AfterFunc` however the subscriber was removed (`TestSubscribe_ContextNoLeak`).
- **`discarding` is set before `release`**, which may close the channel right away. The existing reader discards, never a second one, which would steal messages. The same goes for the single-use `SubscribeSeq` iterator.
- **`add` registers the ctx lifetime before `Store`, and rechecks `ctx.Err()` after**, since the callback may run first and find nothing. It stores between `gate.Enter` and `gate.Leave`, and `Close` closes the gate before ranging, so every subscriber is either refused or seen. Every `Close` ranges, not just the first.
- **No error path the library owns may block.** Errors used to go to a channel returned by `Subscribe`: an unread error blocked the subscriber, then `Broadcast` (`TestSubscribe_ErrorsWithoutHandler`). Only the user's own handler or store may block, and a store the library ships never does.
- **`report` is the only caller of error handlers and `Store.Put`.** It takes no ctx: every error about a message uses `context(m)` (the message's ctx, else the subscriber's), never the `Broadcast` ctx, hence the `contextcheck` nolints in `Deliver`/`send`. `Put` gets `context.WithoutCancel` of it, and a failed `Put` becomes a `StoreError`, so each loss is reported once and stored at most once (`TestSubscriberWithStore_ExactlyOnce`).
- **`report` runs outside the middleware chain** (in `Consume`), so `Recover` never recovers a panic in an error handler.

## Decisions not to revert

- The `Broadcast` ctx only bounds the wait. Async sends keep using it after `Broadcast` returns. That is documented (`message.WithAsync`, `examples/14-context`) rather than changed: detaching them automatically would leave no way to cancel sends piling up behind a stuck subscriber.
- `message.WithContext` replaces the subscriber's ctx for one message, without merging. nil means none, which overrides a default (staticcheck SA1012 is silenced in the test that does it).
- The library applies no middleware of its own. The recommended order is `Recover`, `WrapError`, `Retry`, then the user's. Middlewares don't apply to `SubscribeSeq`.
- `message.Config` has no `T` (`message.Option[T]` keeps it only for the public API), which is why there is no message-level store.
- In `store`, `Enqueue`'s `Put` and `Drain`'s dead-letter `Put` and `Ack` get `context.WithoutCancel`, like `report`'s `Put`: a done ctx is often why a message is stored, and an entry `Drain` handled must be acked even if ctx ended meanwhile (`TestDrain_AckOnceHandled`). An entry whose handle failed once ctx is done stays in the queue, for the next `Drain`.

## Tests

- The root package tests (`broadcastor_test`) cover `subscriber` and `message` too, through a `Broadcastor`, so those have no test files. Name a test after its option, package included: `TestSubscriberWithBuffer`, `TestMessageWithAsync`.
- Most tests run in a `synctest` bubble, where a leaked subscriber goroutine fails the test. So subscribe with `subscribeCtx(t)` (`context.WithoutCancel(t.Context())`): `t.Context()` would unsubscribe a leak instead of failing. `synctest.Wait()` after `Unsubscribe` waits until the subscriber is done. Bound every wait with `deadlockTimeout`.
- A goroutine blocked on a mutex is not durably blocked, so `synctest.Wait()` would hang: `pkg/gate` tests and `TestUnsubscribe_SeveralThenDrain` run in real time.
- Add `Recover` or `WrapError` only to check a `PanicError` or `HandleError`. Other tests check `handle`'s raw error.
- Examples (`examples/NN-name/`, listed in the README) each have an `Example()` in `main_test.go`. They run in real time, so their output must not depend on scheduling: synchronise with channels or a `WaitGroup`, or use `// Unordered output:`. A slow `handle` waits on a `release` channel, never sleeps.
- `BenchmarkBroadcast` waits for every subscriber on each op, and subscribes with `context.WithoutCancel(b.Context())`, since `b.Context()` is done before `Cleanup`.
