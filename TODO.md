# TODO

Ideas for where broadcastor could go, by horizon. Short-term items polish what exists without changing how messages
are delivered. Mid-term items add delivery modes. Long-term items take the library beyond a single process.

Any change to closing or delivery has to keep the rules in CLAUDE.md: only `release` closes a channel, neither
`Unsubscribe` nor `Broadcast` ever waits for another `Broadcast`, and no error or storage path the library owns may block
when nobody reads it.

## Short-term: ready for a v0.1.0 tag

### Docs and hygiene

- [ ] Check whether `go 1.26.1` is really needed. Nothing obvious uses anything newer than 1.25 (`testing/synctest` is GA
  since 1.25), and a lower directive lets more modules import this one. CI reads the version from `go.mod`, so it follows.

### API consistency

- [ ] `Unsubscribe`'s ctx is unused. Give it a job or drop it before v1.

### Small options

- `subscriber.WithFilter(func(T) bool)`: skip unwanted messages before sending, so they never take a reference or hold
  `Broadcast` up.

### Done

- Package doc (`doc.go`) with the delivery guarantees, and doc comments on every exported identifier.
- Runnable examples (`example_test.go`): basic fan-out, error handler, async, timeout, a subscriber unsubscribing itself
  from `handle`.
- README with a table comparing sync, buffered, async and non-blocking delivery, and one of the error types.
- LICENSE (MIT).
- CI (`.github/workflows/ci.yml`) running `make check` with a pinned golangci-lint, and a committed `.golangci.yml`
  (`default: all`, as in the other repos).
- `SubscriberNotFoundError.SubscriberID` is exported.
- Sentinels `ErrSubscriberNotFound`, `subscriber.ErrClosed`, `subscriber.ErrTimeout`, plus `subscriber.ErrDropped` and
  `subscriber.ErrPanic` for the new errors, matched by each typed error's `Is` method.
- `message.WithSync`. Sync, async and non-blocking are now one delivery mode per message, and the last option wins.
- `Broadcast` returns how many subscribers it handed the message to: sync and non-blocking sends taken, plus async sends
  started.
- Recover (now `middleware.Recover`): a panic in `handle` is reported as a `*PanicError` (value and stack), and consuming
  goes on.
- `message.WithNonBlocking` (name kept over `WithFireAndForget`, which reads like async): a busy subscriber misses
  the message with a `*DroppedError`. Note that with a buffer this keeps the oldest messages and drops the newest, so
  it is not "latest value wins". That needs the `DropOldest` policy from "Ordered async" below.
- `SubscribeSeq`, a pull-style variant returning `iter.Seq[T]`: the loop body takes the place of `handle`, reading the
  subscriber's channel directly. Breaking out of the loop, or its ctx being done, unsubscribes. Messages it took but
  never yielded are reported as `*subscriber.ClosedError`.
- `WithUnsubscribeDiscard` (not "drain", which in NATS means the opposite: process what is pending, then close): once
  unsubscribed, the subscriber reports what it takes as `*subscriber.ClosedError` instead of handling it, and a
  `SubscribeSeq` loop ends. It can be the subscriber's default (`subscriber.WithDefaultUnsubscribeOptions`), which is
  how `Close` applies it: `Close()` keeps its signature, and so still satisfies `io.Closer`. `WithUnsubscribeDeliver`
  overrides that default.
- `subscriber.WithAutoUnsubscribe` (since made the default, see below): the subscriber is unsubscribed as soon as the
  `Subscribe` or `SubscribeSeq` ctx is done, even while `handle` runs. It is a `context.AfterFunc` rather than a watcher goroutine, stopped by whichever
  removal comes first, so nothing is left waiting on a ctx that is never done.
- Benchmarks (`bench_test.go`, `make bench`): `Broadcast` for 1 to 1000 subscribers × sync/parallel/async/buffered/middleware,
  and `Subscribe`/`Unsubscribe` churn, serial and parallel.
- `subscriber.WithMiddleware`: a `func(next Handler[T]) Handler[T]` chain around `handle`, first outermost. Breaking
  changes since v0.1.0: `handle` takes the subscriber's ID (`func(ctx, id, msg) error`), so a middleware can build
  errors about it and `handle` can unsubscribe itself without capturing the ID `Subscribe` returns. `WithSubscriberRecover`
  became `middleware.Recover`, and `*HandleError` wrapping is opt-in with `middleware.WrapError`: without it, error
  handlers get `handle`'s error as is.
- Packages `subscriber` and `message`. Breaking change: the options moved next to what they configure and lost their
  prefix (`WithSubscriberBuffer` is `subscriber.WithBuffer`, `WithMessageAsync` is `message.WithAsync`,
  `SubscriberOption` is `subscriber.Option`, `MessageOptions` is `message.Option`). `Handler`, `Middleware` and every
  error about a subscriber's messages moved to `subscriber` too, where `SubscriberClosedError` and
  `ErrSubscriberClosed` became `ClosedError` and `ErrClosed`. `broadcastor` keeps `Broadcastor`, `ErrClosed` and
  `SubscriberNotFoundError`. The subscriber's reference counting stays unexported inside `subscriber`.
- `message.WithParallel`, between sync and async: `Broadcast` sends to every subscriber at once, each from its own
  goroutine, and returns once each has taken the message or missed it. A slow subscriber holds up nobody else,
  successive `Broadcast`s from one goroutine still arrive in order, and the ctx deadline and the timeout apply to every
  subscriber from the same moment. `Broadcast` counts the parallel sends that were taken. `Subscriber.Deliver` now also
  returns a channel for a parallel send, so the other modes allocate nothing more.
- `middleware.Retry(RetryPolicy)`: calls `handle` again after an error, up to `Attempts` calls, waiting `Delay` and then
  each wait times `Multiplier`, capped at `MaxDelay` and shortened at random by up to `Jitter`, for the errors
  `IsRetryable` accepts. It returns the last error as is, and a done `Subscribe` ctx ends it. It waits in the
  subscriber's goroutine, holding the subscriber up like a slow `handle` does. `examples/12-middleware` uses it in place
  of its hand-written retry.
- The `Subscribe`/`SubscribeSeq` ctx is the subscription's lifetime: once it is done, the subscriber is unsubscribed.
  Breaking change: that used to be opt-in with `subscriber.WithAutoUnsubscribe`, which is gone, and left a `Subscribe`
  subscriber calling `handle` with a done ctx. `subscriber.WithDetachedContext` opts out: the subscriber stays
  subscribed, and runs with `context.WithoutCancel` of its ctx, so `handle`, `middleware.Retry` and a `SubscribeSeq` loop
  never see it done. `Subscriber.AutoUnsubscribe` is now `Subscriber.ContextLifetime`, and returns that ctx.
- `message.WithContext(ctx)`: the message is handled with `ctx` instead of the subscriber's ctx, which it replaces
  rather than merging with (so no custom `context.Context`). `handle`, its middlewares and the error handlers get it
  as is, cancellation included. Breaking change: the error handlers now get every error about a message with the ctx it
  is handled with (its own, or else the subscriber's), never the `Broadcast` ctx, which only bounds the wait. That makes
  the ctx the same whatever the error, and no longer done just because `Broadcast` gave up. The trap of an async send
  cancelled with the request that broadcast it is documented, with `context.WithoutCancel` + `message.WithTimeout` +
  `message.WithContext` as the fix (`examples/14-context`), rather than detaching async sends from the `Broadcast` ctx.
- `subscriber.WithStore(Store[T])`: every message a subscriber loses (the errors `handle` returns, panics with
  `middleware.Recover`, timeouts, drops, discards) goes to the store's `Put` as a `subscriber.Record` (subscriber ID,
  message, error), right before the error handlers, so each message a `Broadcast` picks the subscriber up for is either
  handled or stored, exactly once. `Put` gets the values of the message's ctx, but a ctx that is never done. When it
  fails, the handlers get a `*subscriber.StoreError` instead, which matches `ErrStore` and unwraps to both `Put`'s error
  and the original one. The core only writes: reading back is under "Storage" below. `examples/15-store`.

## Mid-term: more delivery modes (v0.x)

### Subscription handle and lifecycle

- `Subscribe` returns a `*Subscription` (`ID()`, `Unsubscribe()`, `Done() <-chan struct{}` closed once `consume`
  returns) instead of a bare `uuid.UUID`. With `Done()`, a caller outside `handle` can wait for the discard themselves,
  and the library still never waits.
- `Broadcastor.Close()` exists: it unsubscribes everyone, refuses new `Subscribe` calls with `ErrClosed` (a `Broadcast`
  after it reaches nobody and returns 0), and never waits. Still missing: a way to wait until the subscribers have
  discarded, bounded by a ctx (`Close(ctx)`, or a separate `Wait(ctx)`). Called from `handle`, that would end up waiting
  on itself, so it has to either detect that case or be documented as off-limits there.

### Ordered async and overflow policies

- Async gives up ordering to avoid blocking. A per-subscriber queue, discarded by one sender goroutine, keeps both:
  `Broadcast` enqueues and returns, and the subscriber gets messages in `Broadcast` order. The queue must be bounded,
  with an overflow policy (`Block`, `DropNewest`, `DropOldest`, `Error`), since an unbounded one just turns a slow
  subscriber into a memory problem. This generalises `subscriber.WithBuffer` and `message.WithNonBlocking`.

### Storage, retry, errors and groups (from the original list)

- Reading back what `subscriber.WithStore` stored, in a `store` package that imports `subscriber` (like `middleware`).
  This is what delayed or persistent retries need: `middleware.Retry` waits in the subscriber's goroutine, so it only
  suits a few quick attempts.
  - [ ] `store.Queue[T]`: `subscriber.Store[T]` plus `Next(ctx) (Entry[T], error)` and `Ack(ctx, id string) error`.
    `Next` returns the oldest entry not yet acked, waiting until there is one, and the same one until it is acked, so
    a single reader keeps them in order. `Entry` adds an ID the store assigns, opaque so that a Redis stream ID or a
    SQLite rowid both fit.
  - [ ] `store.NewRing[T](size)`: in memory and bounded. `Put` never blocks and never fails: when full, it drops the
    oldest entry and counts it (`Dropped()`). That can be the entry `Next` returned, whose `Ack` is then a no-op.
    `Next` waits on a channel `Put` closes and replaces, not a `sync.Cond`, so it can select on `ctx.Done()` and stays
    durably blocked in a synctest bubble.
  - [ ] `store.Drain(ctx, q, handle, deadLetter)`: `Next`, `handle`, `Ack`. An entry `handle` fails on goes to
    `deadLetter`, if set, and is acked. Once ctx is done, the entry is left unacked and `Drain` returns. Retries come from
    wrapping `handle` in `middleware.Retry` (`Attempts: math.MaxInt` to wait for an uplink to come back). `handle` is
    given the ID of the subscriber that lost the message.
  - [ ] `store.Enqueue(q) subscriber.Handler[T]`, a `handle` that only calls `Put`. With `Drain`, that is
    store-and-forward: the subscriber is almost always idle, and the sink gets every message in order from one
    goroutine.
  - [ ] `store.Filter(s, func(error) bool)`, to keep permanent failures (a payload that does not decode) out of a store.
  - [ ] Before tagging, try a SQLite `Queue` in a scratch branch, to check that the interface holds for a durable
    backend.
- `WithErrorStorage`: the old error channel done safely. That means a bounded store (a ring buffer that drops the oldest
  errors and counts the drops) which the user reads whenever they like and which never blocks when nobody reads it (see
  `TestSubscribe_ErrorsWithoutHandler`). It could simply be a ready-made error handler (`NewErrorBuffer(n)` returning the
  handler and a reader), so the library gains no new error path.
- `WithGroup` has two plausible meanings, and both are probably worth having under separate names:
  - **Consumer groups** (`subscriber.WithGroup(name)`): subscribers in the same group share the load. Each message goes
    to only one member of the group (round-robin, or the first one free), while every group and every ungrouped
    subscriber still gets every message. These are Kafka/NATS queue-group semantics, and they turn the fan-out into a
    fan-out plus a work queue.
  - **Batching** (`SubscribeBatch(ctx, handle func(ctx, []T) error, size, maxWait)`): `handle` gets up to `size`
    messages at a time, flushed once full or after `maxWait`. That spreads fixed costs such as a DB round-trip over many
    messages. It needs its own entry point because the `handle` signature changes.

### Replay for late subscribers

- `WithReplay(n)` on the `Broadcastor`: a new subscriber first gets the last `n` messages, and `n = 1` gives "current
  value" semantics. It can reuse `store.Ring`.

### Topics

- Subscribing to a subset of messages: either `subscriber.WithFilter`, or a keyed `Topics[K comparable, T]` holding one
  `Broadcastor[T]` per key, with prefix or wildcard matching later on.

### Performance

- [-] `Broadcast` walks a `sync.Map` through a closure on every call. For workloads that broadcast often but subscribe
  rarely, a copy-on-write `atomic.Pointer[[]*subscriber.Subscriber[T]]` avoids both the map walk and the allocation.
- [-] `Broadcast` allocates once per subscriber (48 B since `message.Config.Context`, 24 B before), even in sync mode. The per-subscriber `message.Config` is moved to
  the heap because `option(&config)` in `message.New` passes its address to an unknown func (`go build -gcflags=-m`). Applying the
  options once per `Broadcast`, recording which fields they set, and then merging those over each subscriber's
  defaults by value could avoid it.
- [ ] Async starts one goroutine per subscriber per `Broadcast`. The per-subscriber sender from "Ordered async" would cap
  that.
- Replace UUIDs with an atomic counter. That removes the only dependency and the only error `Subscribe` can return, but
  changes `handle`'s signature too, since it takes the ID. The distributed direction below would need IDs that are
  unique across processes, though, so decide on that first.

## Long-term: beyond one process

### Observability

- A `Stats()` snapshot: per-subscriber queue length, counts of messages delivered, timed out, dropped and failed, and
  `handle` latency. OpenTelemetry metrics and tracing live in a separate module (`broadcastor/otel`), so the core stays
  at one dependency or none.

### Stronger delivery guarantees

- Acknowledgements: `handle` acks explicitly, and unacked messages go back to storage after a visibility timeout. That
  moves delivery from at-most-once (today) to at-least-once. It needs a message envelope visible to `handle` (ID,
  timestamp, attempt count).
- Dead letters: `subscriber.WithStore` already stores what `handle` still fails on once `middleware.Retry` gives up.
  What is left is `store.Drain`'s `deadLetter`, for what fails again when read back.

### Pluggable transport

- A `Transport` interface behind `Broadcast`, so the same API can fan out across processes: in-process (today's
  behaviour and the default), Redis pub/sub or streams, NATS, Postgres `LISTEN/NOTIFY`. Each backend goes in its own
  module to keep the core's dependencies minimal, and a `Codec[T]` handles serialization.
- Durable backends for `subscriber.WithStore` (`store.Queue`) and `WithReplay` (SQLite, Postgres, Redis streams), built the same way.

### Toward v1.0

- Freeze the API once the subscription handle, the error sentinels and the ctx semantics are settled. Write the delivery
  guarantees down as a contract with one test per guarantee, and follow semver from then on.
- Fuzz or property tests over random interleavings of `Subscribe`, `Unsubscribe` and `Broadcast`, checking the
  invariants: no send on a closed channel, no leaked goroutine, and every subscriber a `Broadcast` picks up either gets
  the message or has it reported, exactly once.
