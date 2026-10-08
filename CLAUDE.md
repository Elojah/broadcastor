# CLAUDE.md

Commands, fragile invariants and settled decisions. Behaviour is in godoc and the README, plans in TODO.md.

## Overview

`github.com/elojah/broadcastor`, a generic in-process fan-out. Go 1.26.1, one dependency: `github.com/google/uuid`.

Packages: `broadcastor` (`Broadcastor`), `subscriber` (`Subscriber`, its options, `Handler`/`Middleware`, `Store`/`Record`, `Stats`, errors), `message` (`Message`, `Config`, `Delivery`, `Broadcast` options), `middleware`, `store`, `filter`, `pkg/gate`, `examples/`. Imports go `broadcastor` → `subscriber` → `message`. `middleware` and `store` import `subscriber`, never `broadcastor`. `filter` imports nothing from the library.

`examples/19-redis`, `24-mqtt` and `25-modbus` are modules of their own (`replace ../..`), so their dependencies stay out of go.mod. `go test ./...` skips them, `make` covers `MODULES`, and `go test -C examples/19-redis ./...` tests one.

## Commands

```sh
go test -race ./...                 # always -race
go test -race -run TestName ./...
make check                          # golangci-lint + tests (-shuffle=on -cpu 1,4) + each benchmark once
make stress                         # TestStress 50 times (STRESS_COUNT)
make bench                          # BENCH=regexp
make benchcmp                       # main (BENCH_BASE) vs the working tree, with benchstat
make tinygo                         # tinygo-test (pkg/gate), tinygo-run (examples, output checked), tinygo-build (Pico)
```

Lint (`.golangci.yml`) is `default: all` minus a disable list, tests included. Mind `nlreturn`, `paralleltest` (`t.Parallel()` first), `forcetypeassert`, `err113`, `godot` and `funcorder` (exported methods first). CI runs `make check` with a pinned golangci-lint in `./bin`.

## Conventions

- Comments are short: invariants, non-obvious whys, the public contract. Don't restate the code or narrate history. State each fact once, where it belongs: a caveat goes on the option it is about, not on everything it touches.
- Options have no `WithSubscriber`/`WithMessage` prefix: `subscriber.WithBuffer`, `message.WithAsync`.
- `Subscriber` exports only what `Broadcastor` calls. Reference counting, `report`, `pull` and `relay` stay unexported, so the invariants hold whatever a caller does.
- Errors are typed structs with a `SubscriberID` and an `Is` matching one sentinel, so `errors.Is` works without `T`. `broadcastor.ErrClosed` and `subscriber.ErrClosed` are different sentinels.

## Invariants

A change to channels, removal or error reporting must keep these, and pass `make stress`.

- **Only `release` closes `ch`.** The subscription holds a reference, and so does each send (`acquire` in `Deliver`). `acquire` refuses at 0, so a `Broadcast` racing removal skips the subscriber with a `ClosedError`.
- **Only `Subscriber.Unsubscribe` closes `done`**, once. `send` checks `done` before its select, which picks at random, so only a send racing `Unsubscribe` gets through, and a waiting `Broadcast` gives up (`TestUnsubscribe_FreesBroadcast`).
- **Nothing waits for a `Broadcast`**, since `handle` may unsubscribe while one waits on it (`TestUnsubscribe_SelfDuringBroadcast`, `TestClose_FromHandle`). `Shutdown` waits only through `Consume`, which `Unsubscribe` frees at once (`TestShutdown_FreesBroadcast`).
- **Every removal goes through `remove`** (`LoadAndDelete`, then `Subscriber.Unsubscribe`), so racing `Unsubscribe`, `Close`, ctx-done and eviction act once. `Subscriber.Unsubscribe` always stops the ctx `AfterFunc` (`TestSubscribe_ContextNoLeak`).
- **Eviction is reported once**, by the error whose `remove` deleted the subscriber, after removing, before `done` and before `Shutdown` returns (`TestSubscriberWithEvict_Once`). Losses are evicted from the `Broadcast`'s goroutine before the send drops its reference, handle's errors from `process`, outside the chain. `evict` never gets a `ClosedError` (`TestSubscriberWithEvict_SubscribeSeqPanic`).
- **`discarding` is set before `release`**, which may close `ch` at once, so the existing reader discards, not a second one.
- **Only `Consume` reads `ch`**, `SubscribeSeq`'s too: its handle, the `relay`, passes each message to the loop and waits for the body's error. An ending loop sets `discarding` before closing `stopped` (and, on a break, before sending the body's error), so only the message in handle goes through the middlewares, as a `ClosedError`. `pull` never calls `yield` after a break or a panic.
- **`add` calls `Attach` before `Store`, and rechecks `ctx.Err()` after**, since the ctx watch may run first and find nothing. It stores between `gate.Enter` and `gate.Leave`, and `Close` closes the gate before ranging, so each subscriber is refused or seen. Every `Close` ranges.
- **`running` goes up only in `add`, under the gate**, from one that only the first `Close` drops, by sending `uuid.Nil` on the Broadcastor's `done`, so it reaches 0 once, after `Close`. Only `drain` receives on `done`: it drops one per ID and closes `stopped` at 0. `add` appends the Broadcastor's `WithDone` last, so `finish` sends on the caller's channels first (`TestSubscriberWithDone_Shutdown`). A `SubscribeSeq` never ranged holds `Shutdown` until its ctx is done (`TestShutdown_SubscribeSeqNotRanged`), and `drain` for good.
- **`Consume` replays before reading `ch`**, through `process`, and checks `discarding` before each value.
- **No error path the library owns may block** (`TestSubscribe_ErrorsWithoutHandler`). Only the user's handlers and stores may. The `done` sends wait only until their `WithDone` ctx is done (`TestSubscriberWithDone_Shutdown`). The Broadcastor's never gives up, since `drain` always receives.
- **Only `report` calls error handlers and `Store.Put`**, outside the middleware chain, with the message's ctx or else the subscriber's, never the `Broadcast` one: hence the `contextcheck` nolints. `Put` gets it `WithoutCancel`, and a failed `Put` becomes a `StoreError`, so each loss is reported once and stored at most once (`TestSubscriberWithDeadLetters_ExactlyOnce`).
- **Each message a `Broadcast` picks a subscriber up for is counted once**, in `Handled`, `Failed`, `TimedOut` or `Dropped`, before `report` (`TestStats_AddUp`). One filtered out, by `message.WithSubscriberFilter` then `subscriber.WithFilter`, is not picked up. `Delivered` counts after the send, so it may briefly trail `Handled`.

## Decisions not to revert

- **Async sends keep the `Broadcast` ctx** after it returns: detaching them would leave no way to cancel sends piling up behind a stuck subscriber.
- **`send` tries a send without waiting before its select**: `selectgo` locks every channel it waits on, so concurrent `Broadcast`s sharing a ctx serialised on its `Done`, up to 10× slower.
- **`WithAsyncLimit` is unlimited by default until v1.0**: a limit drops silently without an error handler, whereas `Stats.Sending` shows a pile-up.
- **`Broadcast` applies its options once** (`message.NewConfig`), and `message.New` lays the fields they set over each subscriber's defaults by value, so a `Broadcast` without options allocates nothing in sync, buffered and non-blocking modes (`TestBroadcast_Allocs`).
- **`message.WithContext` replaces the ctx, without merging.** nil means none, overriding a default (SA1012 is silenced in its test).
- **No middleware is built in.** The recommended order is `Recover`, `History`, `MaxAge`, `WrapError`, `Retry`, then the user's.
- **`SubscribeSeq` runs `Consume` and a `relay`**, rather than reading `ch` in the loop: middlewares around `yield` would break `Recover` (an iterator may not swallow a body panic) and any middleware calling next after a break. It costs a goroutine per `SubscribeSeq` and two handoffs per message. The body gets `fail`, no ctx.
- **`Stats` covers only the subscribers in the map.** Totals across unsubscribes would need shared counters or a fold at removal racing late `ClosedError`s. OpenTelemetry goes in its own module.
- **`Shutdown` waits on a count and a channel**, not a `sync.WaitGroup`, whose `Wait` cannot select on ctx.
- **`WithReplay` takes an `iter.Seq[T]`**, not a queue: the iterator snapshots and acks once `yield` returns, so `subscriber` needs no queue interface.
- **A subscriber reconnects in place** (`handle` or the user's middlewares dial again, `Retry` retries, the buffer keeps order), not by evicting and resubscribing, which loses what is broadcast in between. A suspended state with a hand-over under one ID was tried, and dropped as too intrusive.
- **Connections are the user's**: the library defines no disconnect error nor reconnect middleware, only generic pieces such as `RetryPolicy.Do`. `26-reconnect` builds its own.
- **`WithEvict` takes a predicate on the error, and keeps no count**: losses and handle's errors come from different goroutines, so one built-in count would mix both. Counting goes in a middleware or in `evict`.
- **`WithEvict` sends on no channel**: the error handlers get the `*EvictedError` already.
- **`WithDone` waits for a receiver until its ctx is done**, with the ID, so subscribers can share one. `Shutdown` waits for the send.
- **`remove` is `Attach`'s callback, not a channel**: it deletes from the map only the Broadcastor owns, and returns whether it did, which keeps eviction to once.
- **Subscribers are counted out through the Broadcastor's own `WithDone` channel, which `drain` reads**: a blocking send needs a reader outside `Shutdown`, and a never-done ctx, or a subscriber removed by its ctx would drop its ID.
- **`message.Config` has no `T`** (`message.Option[T]` keeps it only for the API), hence no message-level store.
- **`store` detaches ctx** for `Enqueue`'s `Put`, and `Drain`'s dead-letter `Put` and `Ack`: a done ctx is often why a message is stored, and an entry handled must be acked (`TestDrain_AckOnceHandled`).

## Tests

- The root tests (`broadcastor_test`) cover `subscriber` and `message` through a `Broadcastor`. Name a test after its option, package included: `TestSubscriberWithBuffer`, `TestMessageWithAsync`.
- Most tests run in a `synctest` bubble, where a leaked goroutine fails the test. Create a Broadcastor with `newBroadcastor(t)`, which closes it in Cleanup so that `drain` ends, and range every `SubscribeSeq`. Subscribe with `subscribeCtx(t)` (`context.WithoutCancel(t.Context())`), since `t.Context()` would unsubscribe the leak instead. `synctest.Wait()` after `Unsubscribe` waits until the subscriber is done. Bound every wait with `deadlockTimeout`.
- `synctest.Wait()` returns while `handle` sleeps: sleep in the test too before checking `Stats`. It hangs on a goroutine blocked on a mutex, so `pkg/gate` tests and `TestUnsubscribe_SeveralThenDrain` run in real time.
- `TestBroadcast_Allocs` does not call `t.Parallel()`: `AllocsPerRun` counts every goroutine's allocations.
- `TestStress` runs in real time, so that timeouts race sends, and checks what holds under any schedule. Each round's goroutines carry a pprof label, which the library's inherit, so it can check none is left once closed. It polls until `Stats` add up rather than sleeping.
- Add `Recover` or `WrapError` only to check a `PanicError` or `HandleError`. Other tests check `handle`'s raw error.
- Each example (`examples/NN-name/`, listed in the README) has an `Example()` in `main_test.go` that needs no external service: `19-redis` uses miniredis, `24-mqtt` an in-process mochi-mqtt, `25-modbus` its simulated PLC. mochi and simonvetter/modbus log to stdout by default, which breaks the output. Output must not depend on scheduling: synchronise with channels or a `WaitGroup`, or use `// Unordered output:`. A slow `handle` waits on a `release` channel, never sleeps.
- TinyGo runs no `Example`, so `make tinygo-run` diffs each example's `tinygo run` with its output block, which must not depend on the runtime either: `04-recover` panics itself, since TinyGo words a divide by zero differently.
- `BenchmarkBroadcast` subscribes with `context.WithoutCancel(b.Context())`, since `b.Context()` is done before `Cleanup`.
- `BenchmarkBroadcast_Throughput`, `_Concurrent` and `_Churn` wait once, after the last `Broadcast`, so they loop over `b.N`, not `b.Loop`, which would stop the timer before that wait: don't modernize them.
