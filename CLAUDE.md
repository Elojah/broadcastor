# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository. Behaviour is documented in godoc and the README. This file holds only the commands, the invariants that are easy to break, and decisions not to revert.

## Overview

`github.com/elojah/broadcastor` is a Go library: a generic in-process fan-out, where one `Broadcast` hands a message to every subscriber. Its only dependency is `github.com/google/uuid`. Go 1.26.1.

- `broadcastor` (root): `Broadcastor`, `ErrClosed`, `SubscriberNotFoundError`.
- `subscriber`: `Subscriber`, the options for `Subscribe`/`SubscribeSeq`/`Unsubscribe`, `Handler`/`Middleware`, `Store`/`Record`, `Stats`, and the errors about messages.
- `message`: `Message`, `Config`, `Delivery`, the options for `Broadcast`.
- `middleware` (`Recover`, `History`, `WrapError`, `Retry`), `store` (`Queue`, `Ring`, `Drain`, `Enqueue`, `Filter`), `pkg/gate`, `examples/`.

Imports go one way: `broadcastor` → `subscriber` → `message`. `middleware` and `store` import `subscriber`, never `broadcastor`. Planned work is in TODO.md.

`examples/19-redis` is a module of its own (`replace` to `../..`), so that go-redis and miniredis stay out of the library's go.mod. `go test ./...` at the root skips it: `make` lists it in `MODULES`, and `go test -C examples/19-redis ./...` tests it alone.

## Commands

```sh
go test -race ./...                 # always -race: the package is all concurrency
go test -race -run TestName ./...
make check                          # golangci-lint + tests (-shuffle=on -cpu 1,4) + each benchmark once
make stress                         # TestStress 50 times (STRESS_COUNT)
make bench                          # BENCH=regexp
make benchcmp                       # main (BENCH_BASE) vs the working tree, with benchstat
```

Lint (`.golangci.yml`) is `default: all` minus a disable list, tests included: `nlreturn`, `paralleltest` (`t.Parallel()` first), `forcetypeassert`, `err113`, `godot`, `funcorder` (exported methods first). CI installs a pinned golangci-lint into `./bin` and runs `make check`.

## Conventions

- Comments are short: invariants, non-obvious whys, and the public contract. Don't restate the code, and state each fact once.
- Options have no `WithSubscriber`/`WithMessage` prefix, since the package name says it: `subscriber.WithBuffer`, `message.WithAsync`.
- `Subscriber` exports only what `Broadcastor` calls. The reference counting, `report`, `pull` and `discard` stay unexported, so the invariants below hold whatever a caller does.
- Errors are typed structs with a `SubscriberID`. Each type the library creates has an `Is` matching one sentinel, so `errors.Is` works without `T`. `broadcastor.ErrClosed` and `subscriber.ErrClosed` are different sentinels.

## Invariants

Any change to channels, removal or error reporting must keep these, and pass `make stress`.

- **Only `release` closes a channel.** The subscription holds one reference, and each `Broadcast` sending to a subscriber holds another (`acquire` in `Deliver`). `acquire` refuses a count of 0, so a `Broadcast` that picked a subscriber up just before its removal skips it (`ClosedError`) instead of sending on a closed channel.
- **Only `Subscriber.Unsubscribe` closes `done`**, once, since only whoever removed the subscriber calls it. `send` checks `done` first, since a select would pick at random, so an unsubscribed subscriber takes no new message but one racing `Unsubscribe`, and a `Broadcast` waiting on it gives up with a `ClosedError` (`TestUnsubscribe_FreesBroadcast`).
- **Nothing waits for a `Broadcast`**: not `Unsubscribe`, `Close`, the ctx-lifetime removal, nor another `Broadcast`. `handle` often unsubscribes while a `Broadcast` waits on it, and the subscriber is not reading then (`TestUnsubscribe_SelfDuringBroadcast`, `TestUnsubscribe_SeveralThenDrain`, `TestClose_FromHandle`).
- **Every removal goes through `remove`** (`LoadAndDelete` + `Subscriber.Unsubscribe`), so racing `Unsubscribe`, `Close`, ctx-done and eviction drop the reference and apply the unsubscribe options once. `Subscriber.Unsubscribe` stops the ctx-lifetime `AfterFunc` however the subscriber was removed (`TestSubscribe_ContextNoLeak`).
- **Only the loss whose `remove` deleted the subscriber reports its eviction**, and it removes before it reports. So `*EvictedError` is reported once, never for a subscriber something else removed first, and an error handler that unsubscribes cannot race it (`TestSubscriberWithEvictAfter_Once`).
- **`discarding` is set before `release`**, which may close the channel right away. The existing reader discards, never a second one, which would steal messages. The same goes for the single-use `SubscribeSeq` iterator.
- **`add` registers the ctx lifetime (`Attach`) before `Store`, and rechecks `ctx.Err()` after**, since the callback may run first and find nothing. It stores between `gate.Enter` and `gate.Leave`, and `Close` closes the gate before ranging, so every subscriber is either refused or seen. Every `Close` ranges, not just the first.
- **No error path the library owns may block.** Errors used to go to a channel returned by `Subscribe`: an unread error blocked the subscriber, then `Broadcast` (`TestSubscribe_ErrorsWithoutHandler`). Only the user's own handler or store may block, and a store the library ships never does.
- **`report` is the only caller of error handlers and `Store.Put`.** It takes no ctx: every error about a message uses `context(m)` (the message's ctx, else the subscriber's), never the `Broadcast` ctx, hence the `contextcheck` nolints in `Deliver`/`send`. `Put` gets `context.WithoutCancel` of it, and a failed `Put` becomes a `StoreError`, so each loss is reported once and stored at most once (`TestSubscriberWithDeadLetters_ExactlyOnce`).
- **`report` runs outside the middleware chain** (in `Consume`), so `Recover` never recovers a panic in an error handler.
- **Each message a `Broadcast` picks a subscriber up for is counted once** in `Handled`, `Failed`, `TimedOut` or `Dropped`, and a loss is counted before `report`, so an error handler sees it (`TestStats/Failed`, `TestStats_AddUp`). `Delivered` is counted once the send succeeded, so it may briefly trail `Handled`.

## Decisions not to revert

- The `Broadcast` ctx only bounds the wait. Async sends keep using it after `Broadcast` returns. That is documented (`message.WithAsync`, `examples/14-context`) rather than changed: detaching them automatically would leave no way to cancel sends piling up behind a stuck subscriber.
- `send` tries a send without waiting before its select, so a ready subscriber takes the message even once ctx is done. `selectgo` locks every channel it waits on, and concurrent `Broadcast`s often share a ctx, whose `Done` channel then serialised them: the select alone, with `done` added, made `BenchmarkBroadcast_Concurrent` up to 10× slower than with the try.
- `message.WithContext` replaces the subscriber's ctx for one message, without merging. nil means none, which overrides a default (staticcheck SA1012 is silenced in the test that does it).
- The library applies no middleware of its own. The recommended order is `Recover`, `History`, `WrapError`, `Retry`, then the user's. Middlewares don't apply to `SubscribeSeq`.
- `Stats` covers the subscribers still in the map, with no totals across unsubscribes: those would need counters every subscriber goroutine shares, or a fold at removal racing the late `ClosedError`s. So there is no `Closed` counter, since no snapshot could see one. OpenTelemetry goes in its own module.
- `message.Config` has no `T` (`message.Option[T]` keeps it only for the public API), which is why there is no message-level store.
- In `store`, `Enqueue`'s `Put` and `Drain`'s dead-letter `Put` and `Ack` get `context.WithoutCancel`, like `report`'s `Put` and `middleware.History`'s: a done ctx is often why a message is stored, and an entry `Drain` handled must be acked even if ctx ended meanwhile (`TestDrain_AckOnceHandled`). An entry whose handle failed once ctx is done stays in the queue, for the next `Drain`.

## Tests

- The root package tests (`broadcastor_test`) cover `subscriber` and `message` too, through a `Broadcastor`, so those have no test files. Name a test after its option, package included: `TestSubscriberWithBuffer`, `TestMessageWithAsync`.
- Most tests run in a `synctest` bubble, where a leaked subscriber goroutine fails the test. So subscribe with `subscribeCtx(t)` (`context.WithoutCancel(t.Context())`): `t.Context()` would unsubscribe a leak instead of failing. `synctest.Wait()` after `Unsubscribe` waits until the subscriber is done. Bound every wait with `deadlockTimeout`.
- A sleeping `handle` is durably blocked, so `synctest.Wait()` returns before it is done: sleep in the test too before checking `Stats`.
- A goroutine blocked on a mutex is not durably blocked, so `synctest.Wait()` would hang: `pkg/gate` tests and `TestUnsubscribe_SeveralThenDrain` run in real time.
- `TestStress` runs in real time, so that timeouts race sends, and checks what must hold under any schedule. Every goroutine of a round carries a pprof label (`pprof.Do`), which the library's goroutines inherit, so it can check that none is left once closed although other tests run beside it. It polls until `Stats` add up rather than sleeping.
- Add `Recover` or `WrapError` only to check a `PanicError` or `HandleError`. Other tests check `handle`'s raw error.
- Examples (`examples/NN-name/`, listed in the README) each have an `Example()` in `main_test.go`. `19-redis`'s runs against miniredis, in memory, so CI needs no Redis. They run in real time, so their output must not depend on scheduling: synchronise with channels or a `WaitGroup`, or use `// Unordered output:`. A slow `handle` waits on a `release` channel, never sleeps.
- `BenchmarkBroadcast` waits for every subscriber on each op, and subscribes with `context.WithoutCancel(b.Context())`, since `b.Context()` is done before `Cleanup`.
- `BenchmarkBroadcast_Throughput`, `_Concurrent` and `_Churn` wait once, after the last `Broadcast`, so they loop over `b.N` rather than `b.Loop`, which would stop the timer before that wait: don't modernize them. Each subscriber counts its own messages (`subscribeCounting`), since a shared `WaitGroup` would contend. They subscribe with `b.Context()`, which ends after each run of the benchmark function.
