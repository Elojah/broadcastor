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

- Comments are short: invariants, non-obvious whys, the public contract. Don't restate the code. State each fact once.
- Options have no `WithSubscriber`/`WithMessage` prefix: `subscriber.WithBuffer`, `message.WithAsync`.
- `Subscriber` exports only what `Broadcastor` calls. Reference counting, `report`, `pull` and `relay` stay unexported, so the invariants hold whatever a caller does.
- Errors are typed structs with a `SubscriberID` and an `Is` matching one sentinel, so `errors.Is` works without `T`. `broadcastor.ErrClosed` and `subscriber.ErrClosed` are different sentinels.

## Invariants

A change to channels, removal or error reporting must keep these, and pass `make stress`.

- **Only `release` closes a channel.** The subscription holds a reference, and so does each send (`acquire` in `Deliver`). `acquire` refuses at 0, so a `Broadcast` racing removal skips the subscriber with a `ClosedError`.
- **Only `Subscriber.Unsubscribe` closes `done`**, once, called by whoever removed the subscriber. `send` checks `done` before its select, which picks at random, so only a send racing `Unsubscribe` gets through, and a waiting `Broadcast` gives up (`TestUnsubscribe_FreesBroadcast`).
- **Nothing waits for a `Broadcast`**, since `handle` often unsubscribes while one waits on it (`TestUnsubscribe_SelfDuringBroadcast`, `TestUnsubscribe_SeveralThenDrain`, `TestClose_FromHandle`). Only `Shutdown` does, bounded by its ctx, and only through `Consume`, which `Unsubscribe` frees at once (`TestShutdown_FreesBroadcast`).
- **Every removal goes through `remove`** (`LoadAndDelete` + `Subscriber.Unsubscribe`), so racing `Unsubscribe`, `Close`, ctx-done and eviction act once. `Subscriber.Unsubscribe` always stops the ctx `AfterFunc` (`TestSubscribe_ContextNoLeak`).
- **Only the error whose `remove` deleted the subscriber reports its eviction**, after removing, then calls `onEvict`: so once, never for a subscriber removed otherwise, and no unsubscribing error handler can race it (`TestSubscriberWithEvict_Once`). `evict` gets losses in the `Broadcast`'s goroutine, before the send drops its reference, and handle's errors in `process`, outside the chain like `report`, before `onDone`: either way `Shutdown` waits for `onEvict`. It never gets a `ClosedError`: an ended `SubscribeSeq` loop's would race its own removal (`TestSubscriberWithEvict_SubscribeSeqPanic`).
- **`discarding` is set before `release`**, which may close the channel at once, so the existing reader discards, not a second one that would steal messages.
- **Only `Consume` reads the channel**, `SubscribeSeq`'s too: its handle, the `relay`, passes each message to the loop and waits for the body's error, so middlewares, errors and `Stats` work as for `Subscribe`. An ending loop sets `discarding` before closing `stopped` (and, on a break, before sending the body's error), so only the message in handle goes through the middlewares, as a `ClosedError`. `pull` never calls `yield` after a break or a panic.
- **`add` calls `Attach` before `Store`, and rechecks `ctx.Err()` after**, since the callback may run first and find nothing. It stores between `gate.Enter` and `gate.Leave`, and `Close` closes the gate before ranging, so each subscriber is refused or seen. Every `Close` ranges.
- **`running` goes up only in `add`, under the gate**, from one that only the first `Close` drops, so it reaches 0 once, after `Close`. Whoever drops it there closes `stopped`, which `Shutdown` waits on. Each `Consume`, `SubscribeSeq`'s too, drops one through its last `onDone`: `add` appends `WithOnDone(b.stop)` to a copy of the caller's options, so `Shutdown` waits for the caller's `onDone` (`TestSubscriberWithOnDone_Shutdown`). A `SubscribeSeq` never ranged holds `Shutdown` until its ctx is done (`TestShutdown_SubscribeSeqNotRanged`).
- **`Consume` replays before reading `ch`**, through `process`, so a `Broadcast` waits for a replay as for a busy handle. It checks `discarding` before each value and stops without handling it, so `yield` returns false and the source keeps the rest. Without discard it keeps replaying once unsubscribed.
- **No error path the library owns may block.** An unread errors channel once blocked the subscriber, then `Broadcast` (`TestSubscribe_ErrorsWithoutHandler`). Only the user's handlers and stores may block, and no shipped store does.
- **Only `report` calls error handlers and `Store.Put`**, with `context(m)`, the message's ctx or else the subscriber's, never the `Broadcast` one: hence the `contextcheck` nolints. `Put` gets it `WithoutCancel`, and a failed `Put` becomes a `StoreError`, so each loss is reported once and stored at most once (`TestSubscriberWithDeadLetters_ExactlyOnce`).
- **`report` runs outside the middleware chain**, so `Recover` never recovers an error handler's panic.
- **Each message a `Broadcast` picks a subscriber up for is counted once**, in `Handled`, `Failed`, `TimedOut` or `Dropped`. A filtered one (first thing in `Deliver`) is not picked up. A loss is counted before `report`, so error handlers see it (`TestStats/Failed`, `TestStats_AddUp`). `Delivered` counts after the send, so it may briefly trail `Handled`. A replayed value counts in `Delivered` then `Handled` or `Failed`.

## Decisions not to revert

- **Async sends keep the `Broadcast` ctx** after it returns. Documented (`message.WithAsync`, `examples/14-context`), not changed: detaching them would leave no way to cancel sends piling up behind a stuck subscriber.
- **`send` tries a send without waiting before its select**, so a ready subscriber takes the message even once ctx is done. `selectgo` locks every channel it waits on, so concurrent `Broadcast`s sharing a ctx serialised on its `Done`: without the try, `BenchmarkBroadcast_Concurrent` was up to 10× slower.
- **`WithAsyncLimit` is unlimited by default until v1.0**: a limit drops silently without an error handler, whereas `Stats.Sending` shows a pile-up. A refused async send is a `*DroppedError`, counted in `Dropped` and towards eviction.
- **`Broadcast` applies its options once** (`message.NewConfig`), and `message.New` lays the fields they set, recorded in an unexported mask, over each subscriber's defaults by value. Applying them per subscriber moved `&config` to the heap. Now a `Broadcast` allocates nothing without options in sync, buffered and non-blocking modes, and once with options (`TestBroadcast_Allocs`).
- **`message.WithContext` replaces the ctx, without merging.** nil means none, overriding a default (SA1012 is silenced in its test).
- **No middleware is built in.** The recommended order is `Recover`, `History`, `MaxAge`, `WrapError`, `Retry`, then the user's. `MaxAge` goes before `Retry` so that an expired message is not retried, and before `WrapError` since `*ExpiredError` already names the subscriber and the message.
- **`SubscribeSeq` runs `Consume` and a `relay`**, rather than reading the channel in the loop: middlewares around `yield` would crash on `Recover` (an iterator may not swallow a body panic) and on any middleware calling next after a break. It costs a goroutine per `SubscribeSeq` and two handoffs per message: 1.0 to 2.1 µs unbuffered, 0.6 to 1.9 µs with a buffer of 64. `Retry` never retries `ErrClosed`, which an ended loop returns. The body gets `fail`, no ctx.
- **`Stats` covers only the subscribers in the map.** Totals across unsubscribes would need counters every subscriber shares, or a fold at removal racing the late `ClosedError`s, hence no `Closed` counter. OpenTelemetry goes in its own module.
- **`Shutdown` waits on a count and a channel**, not a `sync.WaitGroup`, whose `Wait` cannot select on ctx and would leak a goroutine, for good with a `SubscribeSeq` never ranged. It returns what its `Close` returned.
- **`WithReplay` takes an `iter.Seq[T]`**, not a queue: the iterator snapshots, and acks once `yield` returns. So `subscriber` needs no queue interface, and `slices.Values` gives a late subscriber the current value.
- **A subscriber reconnects in place**: `handle` dials again, `Retry` retries, and the buffer keeps order (`examples/26-reconnect`, `25-modbus`). Evicting and resubscribing loses what is broadcast in between. Closing that gap took a suspended state, an atomic hand-over under one ID (under two, a `Range` may reach both or neither) and a wait between the two goroutines: tried, and dropped as too intrusive.
- **`WithEvict` takes a predicate on the error, and keeps no count.** Losses in a row reset on each message taken, and handle's errors come from another goroutine, so one built-in count would mix both. Counting is the caller's: in a middleware returning an error `evict` matches (`examples/17-evict`), or in `evict`. It is no middleware itself, since a middleware never sees a loss.
- **`onEvict` and `onDone` are callbacks, not channels.** `onEvict` runs before the evicting send drops its reference, so that it can close what a stuck `handle` waits on, and `Shutdown` waits for it. An unread channel would block like the errors channel did. A done channel is `WithOnDone(func() { close(done) })`. Waiting for `onDone` in `onEvict` deadlocks.
- **`message.Config` has no `T`** (`message.Option[T]` keeps it only for the API), hence no message-level store.
- **`store` detaches ctx** (`context.WithoutCancel`) for `Enqueue`'s `Put`, and `Drain`'s dead-letter `Put` and `Ack`, like `report` and `History` do: a done ctx is often why a message is stored, and an entry handled must be acked (`TestDrain_AckOnceHandled`). An entry that failed once ctx is done stays queued for the next `Drain`.

## Tests

- The root tests (`broadcastor_test`) cover `subscriber` and `message` through a `Broadcastor`. Name a test after its option, package included: `TestSubscriberWithBuffer`, `TestMessageWithAsync`.
- Most tests run in a `synctest` bubble, where a leaked goroutine fails the test. So subscribe with `subscribeCtx(t)` (`context.WithoutCancel(t.Context())`): `t.Context()` would unsubscribe the leak instead. `synctest.Wait()` after `Unsubscribe` waits until the subscriber is done. Bound every wait with `deadlockTimeout`.
- `synctest.Wait()` returns while `handle` sleeps, which is durably blocked: sleep in the test too before checking `Stats`. It hangs on a goroutine blocked on a mutex, which is not, so `pkg/gate` tests and `TestUnsubscribe_SeveralThenDrain` run in real time.
- `TestBroadcast_Allocs` does not call `t.Parallel()`: `AllocsPerRun` counts every goroutine's allocations.
- `TestStress` runs in real time, so that timeouts race sends, and checks what holds under any schedule. Each round's goroutines carry a pprof label (`pprof.Do`), which the library's inherit, so it can check none is left once closed. It polls until `Stats` add up rather than sleeping. Each round ends with `Shutdown`, after which nothing may be handled or reported, a quarter of them with messages in flight.
- Add `Recover` or `WrapError` only to check a `PanicError` or `HandleError`. Other tests check `handle`'s raw error.
- Each example (`examples/NN-name/`, listed in the README) has an `Example()` in `main_test.go` that needs no external service: `19-redis` uses miniredis, `24-mqtt` an in-process mochi-mqtt, and `25-modbus` its simulated PLC (`plc.go`) on a free port. mochi and simonvetter/modbus log to stdout by default, which breaks the output. Output must not depend on scheduling: synchronise with channels or a `WaitGroup`, or use `// Unordered output:`. A slow `handle` waits on a `release` channel, never sleeps.
- TinyGo runs no `Example`, so `make tinygo-run` diffs each example's `tinygo run` with its output block, which must not depend on the runtime either: `04-recover` panics itself, since TinyGo words a divide by zero differently.
- `BenchmarkBroadcast` waits for every subscriber on each op, and subscribes with `context.WithoutCancel(b.Context())`, since `b.Context()` is done before `Cleanup`.
- `BenchmarkBroadcast_Throughput`, `_Concurrent` and `_Churn` wait once, after the last `Broadcast`, so they loop over `b.N`, not `b.Loop`, which would stop the timer before that wait: don't modernize them. Each subscriber counts its own messages (`subscribeCounting`), since a shared `WaitGroup` would contend. They subscribe with `b.Context()`, which ends after each run.
