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
- `subscriber.WithAutoUnsubscribe`: the subscriber is unsubscribed as soon as the `Subscribe` or `SubscribeSeq` ctx is
  done, even while `handle` runs. It is a `context.AfterFunc` rather than a watcher goroutine, stopped by whichever
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

## Mid-term: more delivery modes (v0.x)

### Subscription handle and lifecycle

- `Subscribe` returns a `*Subscription` (`ID()`, `Unsubscribe()`, `Done() <-chan struct{}` closed once `consume`
  returns) instead of a bare `uuid.UUID`. With `Done()`, a caller outside `handle` can wait for the discard themselves,
  and the library still never waits.
- `Broadcastor.Close()` exists: it unsubscribes everyone, refuses new `Subscribe` calls with `ErrClosed` (a `Broadcast`
  after it reaches nobody and returns 0), and never waits. Still missing: a way to wait until the subscribers have
  discarded, bounded by a ctx (`Close(ctx)`, or a separate `Wait(ctx)`). Called from `handle`, that would end up waiting
  on itself, so it has to either detect that case or be documented as off-limits there.

### Broadcast ctx reaching `handle`

- [ ] `handle` gets the `Subscribe` ctx, so values on the `Broadcast` ctx (trace IDs, request-scoped loggers) never reach it.
  Carry the `Broadcast` ctx's values on the message, and give `handle` a ctx that combines those values with the
  subscriber's cancellation. This needs a small custom `context.Context`. Offer it as an option, or make it the default
  before v1.

### Ordered async and overflow policies

- Async gives up ordering to avoid blocking. A per-subscriber queue, discarded by one sender goroutine, keeps both:
  `Broadcast` enqueues and returns, and the subscriber gets messages in `Broadcast` order. The queue must be bounded,
  with an overflow policy (`Block`, `DropNewest`, `DropOldest`, `Error`), since an unbounded one just turns a slow
  subscriber into a memory problem. This generalises `subscriber.WithBuffer` and `message.WithNonBlocking`.

### Storage, retry, errors and groups (from the original list)

- `middleware.Retry(RetryPolicy)` (see `examples/12-middleware`): run `handle` again after an error, with backoff
  (attempts, delay, jitter, `IsRetryable(err)`). An in-memory retry holds the subscriber up during backoff, just like a slow `handle` does. That
  is fine for a few quick attempts. Delayed or persistent retries need `WithStorage`.
- `WithStorage(Storage[T])`: an interface (`Put`, `Next`, `Ack`) holding messages that failed or timed out, to be retried
  or replayed later. Ship an in-memory ring buffer first and add real backends later.
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
  value" semantics. It can reuse the ring buffer behind `WithStorage`.

### Topics

- Subscribing to a subset of messages: either `subscriber.WithFilter`, or a keyed `Topics[K comparable, T]` holding one
  `Broadcastor[T]` per key, with prefix or wildcard matching later on.

### Performance

- `Broadcast` walks a `sync.Map` through a closure on every call. For workloads that broadcast often but subscribe
  rarely, a copy-on-write `atomic.Pointer[[]*subscriber.Subscriber[T]]` avoids both the map walk and the allocation.
- `Broadcast` allocates once per subscriber (24 B), even in sync mode. The per-subscriber `message.Config` is moved to
  the heap because `option(&config)` in `message.New` passes its address to an unknown func (`go build -gcflags=-m`). Applying the
  options once per `Broadcast`, recording which fields they set, and then merging those over each subscriber's
  defaults by value could avoid it.
- Async starts one goroutine per subscriber per `Broadcast`. The per-subscriber sender from "Ordered async" would cap
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
- Dead letters: once the retry policy gives up, pass the message to a dead-letter handler or store instead of only
  reporting it.

### Pluggable transport

- A `Transport` interface behind `Broadcast`, so the same API can fan out across processes: in-process (today's
  behaviour and the default), Redis pub/sub or streams, NATS, Postgres `LISTEN/NOTIFY`. Each backend goes in its own
  module to keep the core's dependencies minimal, and a `Codec[T]` handles serialization.
- Durable backends for `WithStorage` and `WithReplay` (SQLite, Postgres, Redis streams), built the same way.

### Toward v1.0

- Freeze the API once the subscription handle, the error sentinels and the ctx semantics are settled. Write the delivery
  guarantees down as a contract with one test per guarantee, and follow semver from then on.
- Fuzz or property tests over random interleavings of `Subscribe`, `Unsubscribe` and `Broadcast`, checking the
  invariants: no send on a closed channel, no leaked goroutine, and every subscriber a `Broadcast` picks up either gets
  the message or has it reported, exactly once.
