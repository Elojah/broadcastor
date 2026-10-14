# CLAUDE.md

Commands, fragile invariants and settled decisions. Behaviour is in godoc and the README, plans in TODO.md.

## Overview

`github.com/elojah/broadcastor`, a generic in-process fan-out. Go 1.26.1, no dependency.

Packages: `broadcastor` (`Broadcastor`), `subscriber` (`Subscriber`, its options, `Handler`/`Middleware`, `Store`/`Record`, `Holder`, `Stats`, errors), `message` (`Message`, `Config`, `Delivery`, `Batch`, `Slice`, `Map`, `Broadcast` options), `middleware`, `store`, `holder`, `filter`, `pkg/gate`, `examples/`. Imports go `broadcastor` → `subscriber` → `message`. `middleware` and `store` import `subscriber`, never `broadcastor`. `holder` imports only `message`, `filter` nothing from the library.

`Subscriber` is split by goroutine: `deliver.go` runs in the `Broadcast`'s, `consume.go` and `holder.go` in the subscriber's, `report.go` in either. `inbox` owns `ch`, `done` and `refs` (`open`, `stop`, `acquire`, `release`), and `counters` every counter, written only through its methods.

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

- **Only `inbox.release` closes `ch`.** The subscription holds a reference, which `inbox.stop` drops, and so does each send (`acquire` in `Deliver`). `acquire` refuses at 0, so a `Broadcast` racing removal skips the subscriber with a `ClosedError`.
- **Only `Subscriber.Unsubscribe` closes `done`**, once, through `inbox.stop`. `send` checks `done` before its select, which picks at random, so only a send racing `Unsubscribe` gets through, and a waiting `Broadcast` gives up (`TestUnsubscribe_FreesBroadcast`).
- **Nothing waits for a `Broadcast`**, since `handle` may unsubscribe while one waits on it (`TestUnsubscribe_SelfDuringBroadcast`, `TestClose_FromHandle`). `Shutdown` waits only through `Consume`, which `Unsubscribe` frees at once (`TestShutdown_FreesBroadcast`).
- **Every removal goes through `remove`** (`LoadAndDelete`, then `Subscriber.Unsubscribe`), so racing `Unsubscribe`, `Close`, ctx-done and eviction act once. `Subscriber.Unsubscribe` always stops the ctx `AfterFunc` (`TestSubscribe_ContextNoLeak`).
- **Eviction is reported once**, by the error whose `remove` deleted the subscriber, after removing, before `done` and before `Shutdown` returns (`TestSubscriberWithEvict_Once`). Losses are evicted from the `Broadcast`'s goroutine before the send drops its reference, a holder's drops from `hold` and handle's errors from `process`, outside the chain. `evict` never gets a `ClosedError` (`TestSubscriberWithEvict_SubscribeSeqPanic`).
- **`discarding` is set before `inbox.stop`**, which may close `ch` at once, so the existing reader discards, not a second one.
- **Only `Consume` reads `ch`**, `SubscribeSeq`'s too: its handle, the `relay`, passes each message to the loop and waits for the body's error. An ending loop sets `discarding` before closing `stopped` (and, on a break, before sending the body's error), so only the message in handle goes through the middlewares, as a `ClosedError`. `pull` never calls `yield` after a break or a panic.
- **`add` calls `Attach` before `Store`, and rechecks `ctx.Err()` after**, since the ctx watch may run first and find nothing. It stores between `gate.Enter` and `gate.Leave`, and `Close` closes the gate before ranging, so each subscriber is refused or seen. Every `Close` ranges.
- **`running` goes up only in `add`, under the gate**, from one that only the first `Close` drops, so it reaches 0 once, after `Close`. `countOut` drops one and closes `stopped` at 0. `finish` calls it, given to `Attach`, after the caller's `WithDone` channels (`TestSubscriberWithDone_Shutdown`). A `SubscribeSeq` never ranged holds `Shutdown` until its ctx is done (`TestShutdown_SubscribeSeqNotRanged`).
- **`Consume` replays before reading `ch`**, through `process`, and checks `discarding` before each value.
- **A batch is whole until unrolled**: one send, one reference, one place in `ch`. Lost in `Deliver` or `send`, or discarded from `ch`, it is reported once, with the batch in the error, and so is the batch handler's error. Without a batch handler, `process` unrolls it into single messages carrying its Config, checking `discarding` before each, and each one's error is its own. `report` puts a batch one value at a time, then calls the error handlers once.
- **Holders get single messages**: `hold` unrolls a batch, releasing what is due before each `Hold` (`TestSubscriberWithHolder_Batch`). `Release` may return a batch, which lowers `Held` by its `Len`.
- **Stats count values, not batches**: `counters` takes `m.Len()` wherever a message is counted, `Batches` for the batch handler's calls. `Queued` adds `batched`, a batch's values past its first, raised after the send and lowered on receipt, so it is clamped at 0.
- **Only `Consume`'s goroutine calls the holder**, so it needs no lock, and passes it the time, so it reads no clock. `pace` releases what is due before each read of `ch`, and before each `Hold` everything due at the `now` it holds at, so a holder never gets a message for a window that ended. Once `ch` is closed it releases everything, before `finish`, so `Shutdown` waits for it (`TestSubscriberWithHolder_Shutdown`). Once discarding, `hold` and `unhold` report as `ClosedError` instead. Without a holder, `Consume` ranges over `ch`, with no timer.
- **No error path the library owns may block** (`TestSubscribe_ErrorsWithoutHandler`). Only the user's handlers and stores may. The `done` sends wait only until their `WithDone` ctx is done (`TestSubscriberWithDone_Shutdown`).
- **Only `report` calls error handlers and `Store.Put`**, outside the middleware chain, with the message's ctx or else the subscriber's, never the `Broadcast` one: hence the `contextcheck` nolints. `Put` gets it `WithoutCancel`, and a failed `Put` becomes a `StoreError`, so each loss is reported once and stored at most once (`TestSubscriberWithDeadLetters_ExactlyOnce`).
- **`store.File`'s positions only grow**: `OpenFile` truncates a segment at its first bad record and appends to no segment from before it. The first `Put` starts one at `max(end of log, ack offset)`, so an ack offset written before a power cut never lands inside a later record (`TestFile_Truncated`, `FuzzOpenFile`). Every segment but the newest holds an entry not acked, so `roll` removes the previous one once acked (`TestFile_Segments`).
- **Each message a `Broadcast` picks a subscriber up for is counted once**, in `Handled`, `Failed`, `TimedOut`, `Dropped` or `Merged`, before `report` (`TestStats_AddUp`). `Held` counts what is in the holder, up in `hold`, down in `unhold`. One filtered out, by `message.WithSubscriberFilter` then `subscriber.WithFilter`, is not picked up. `Delivered` counts after the send, so it may briefly trail `Handled`.

## Decisions not to revert

- **Async sends keep the `Broadcast` ctx** after it returns: detaching them would leave no way to cancel sends piling up behind a stuck subscriber.
- **`send` tries a send without waiting before its select**: `selectgo` locks every channel it waits on, so concurrent `Broadcast`s sharing a ctx serialised on its `Done`, up to 10× slower.
- **The hot path keeps its call depth**: `send`'s selects stay in `Subscriber.send`, not in an `inbox` method, since a second frame under `selectgo` pushed each async goroutine past its initial stack (`Broadcast_Throughput/subscribers=1/async` 1.7× slower). `process` calls handle itself, the discard checks stay inline (a generic helper does not inline), and `counters`' methods take the time rather than read the clock, so they inline: one more call per message cost up to 8% on unrolled batches.
- **`WithAsyncLimit` is unlimited by default until v1.0**: a limit drops silently without an error handler, whereas `Stats.Sending` shows a pile-up.
- **`Broadcast` applies its options once** (`message.NewConfig`), and `message.New` lays the fields they set over each subscriber's defaults by value, so a `Broadcast` without options allocates nothing in sync, buffered and non-blocking modes (`TestBroadcast_Allocs`).
- **Batches travel in `Message.Batch`**, not a channel or a type of their own, so every invariant on `ch` holds as is. A batch has one Config, from its `BroadcastBatch`'s options, or the newest message's for a holder's: that is what keeps the batch handler and the error handlers to one call per batch.
- **`message.Batch` has `Add`, `All`, `Len` and `New`**: `Len` because a batch is counted on every send, loss and handle, and `New` so that a filter keeping part of a batch hands on the producer's type. `keep` calls the filter once per value, since filters keep state, in one pass, since `All` owes no order (`Map` has none).
- **Dead letters get a batch one value at a time**, so stores, `Drain` and `store.File` stay per message.
- **`process` and `hold` call `All()` rather than range over it**: ranging allocates the loop's state too.
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
- **Subscribers are counted out by `countOut`, a callback `Attach` takes, not a channel**, so a Broadcastor runs no goroutine of its own and one never closed is garbage collected (`TestNewBroadcastor_NotClosed`). A channel needed a reader outside `Shutdown`, since a send bounded by the subscriber's ctx would miss a subscriber removed by its ctx.
- **IDs are a `uint64` from a per-Broadcastor atomic counter**, from 1, not UUIDs: no dependency, and `ErrClosed` the only error `Subscribe` returns. They are unique only within a Broadcastor, which works because cross-process transports are out of scope. `message.Map`'s keys come from a counter of their own, shared by every `Map`.
- **`message.Config` has no `T`** (`message.Option[T]` keeps it only for the API), hence no message-level store.
- **Holding is a hook, windows live in holders**: `Consume` only holds and releases through `Holder`, and wakes up on one timer, so a reorder, a delay or a batch can be a holder too. `holder.Reduce` (`NewLatest` too) and `holder.Reorder` keep one window at a time, `holder.Jitter` none. `Hold` returns how many it merged away, not whether, since `Reduce` may merge several at once. `Reduce` reduces as messages come, not at the window's end, so that what it holds stays bounded. Its func sees only values, so every message it releases carries the newest message's `Config`: `Reorder`, whose messages keep theirs, is not a `Reduce`, and sorts once the window ends, stable. `Jitter` releases, once its oldest message is due, every one ordered before it, so that none waits more than d: releasing the first in order once it is due would hold a stream ordered backwards forever. It keeps a heap, tied by arrival, and its arrivals since the oldest held, marking those released early.
- **`store.File` drops the oldest segment once full**, as `Ring` drops its oldest entry, and syncs after each write unless given `WithSyncEvery` or `WithSyncInterval`: batching is opt-in, so the default loses nothing.
- **A stored error keeps its text and a mask of the `subscriber` sentinels it matched** (`sentinels` in `store/segment.go`), so that `errors.Is` holds once read back without a field in `Record`. The table is part of the format: append only.
- **`File.Next` returns a `*DecodeError` rather than skip an entry it cannot decode**, which stays until acked by its ID: a wrong decode func would otherwise drop every entry.
- **`store` detaches ctx** for `Enqueue`'s `Put`, and `Drain`'s dead-letter `Put` and `Ack`: a done ctx is often why a message is stored, and an entry handled must be acked (`TestDrain_AckOnceHandled`).

## Tests

- The root tests (`broadcastor_test`) cover `subscriber` and `message` through a `Broadcastor`. Name a test after its option, package included: `TestSubscriberWithBuffer`, `TestMessageWithAsync`.
- Most tests run in a `synctest` bubble, where a leaked goroutine fails the test. Create a Broadcastor with `newBroadcastor(t)`, which closes it in Cleanup so that every subscriber ends, and range every `SubscribeSeq`. Subscribe with `subscribeCtx(t)` (`context.WithoutCancel(t.Context())`), since `t.Context()` would unsubscribe the leak instead. `synctest.Wait()` after `Unsubscribe` waits until the subscriber is done. Bound every wait with `deadlockTimeout`.
- `synctest.Wait()` returns while `handle` sleeps: sleep in the test too before checking `Stats`. It hangs on a goroutine blocked on a mutex, so `pkg/gate` tests and `TestUnsubscribe_SeveralThenDrain` run in real time.
- `store.SetFsync` (`export_test.go`) lets `File` tests count or fail syncs. They put one-digit messages, 12 bytes a record (`recordSize`).
- `TestBroadcast_Allocs` does not call `t.Parallel()`: `AllocsPerRun` counts every goroutine's allocations.
- `TestStress` runs in real time, so that timeouts race sends, and checks what holds under any schedule. Each round's goroutines carry a pprof label, which the library's inherit, so it can check none is left once closed. It polls until `Stats` add up rather than sleeping.
- Add `Recover` or `WrapError` only to check a `PanicError` or `HandleError`. Other tests check `handle`'s raw error.
- Each example (`examples/NN-name/`, listed in the README) has an `Example()` in `main_test.go` that needs no external service: `19-redis` uses miniredis, `24-mqtt` an in-process mochi-mqtt, `25-modbus` its simulated PLC. mochi and simonvetter/modbus log to stdout by default, which breaks the output. Output must not depend on scheduling: synchronise with channels or a `WaitGroup`, or use `// Unordered output:`. A slow `handle` waits on a `release` channel, never sleeps.
- TinyGo runs no `Example`, so `make tinygo-run` diffs each example's `tinygo run` with its output block, which must not depend on the runtime either: `04-recover` panics itself, since TinyGo words a divide by zero differently.
- `BenchmarkBroadcast_Throughput`, `_Concurrent` and `_Churn` wait once, after the last `Broadcast`, so they loop over `b.N`, not `b.Loop`, which would stop the timer before that wait: don't modernize them.
