package subscriber

import (
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Stats is a snapshot of a subscriber's counters. Each message, each value of a batch, and each replayed value, is
// counted once in Handled, Failed, TimedOut, Dropped or Merged. Delivered is Handled + Failed + Merged + Held + Queued,
// plus the messages a holder had no room for and those taken but not done with: the message or batch handle is on, and
// the rest of a batch handed to it one value at a time. The counters are read one at a time, so they may not add up
// while messages are in flight. For SubscribeSeq, handle is the loop body.
type Stats struct {
	SubscriberID uuid.UUID

	// Queued messages wait in a buffer of size Buffer (WithBuffer), a batch taking one place.
	Queued int
	Buffer int
	// Sending counts the async sends under way (WithAsyncLimit).
	Sending int
	// Held messages wait in the subscriber's holder (WithHolder).
	Held int

	// Delivered counts the messages taken and the values replayed.
	Delivered uint64
	// Handled and Failed count the messages handle, or its outermost middleware, returned nil or an error for.
	Handled uint64
	Failed  uint64
	// TimedOut and Dropped count the messages lost as a *TimeoutError or a *DroppedError.
	TimedOut uint64
	Dropped  uint64
	// Merged counts the messages a holder merged away.
	Merged uint64
	// Batches counts the batches handed to the batch handler (WithBatchHandler).
	Batches uint64

	// HandleTime is the time spent in handle and its middlewares, middleware.Retry's waits included.
	HandleTime time.Duration
	// Handling is how long handle has been running on the current message, 0 when idle.
	Handling time.Duration
}

// Stats returns a snapshot of the subscriber's counters.
func (s *Subscriber[T]) Stats() Stats {
	return Stats{
		SubscriberID: s.id,
		Queued:       max(len(s.inbox.ch)+int(s.counters.batched.Load()), 0),
		Buffer:       cap(s.inbox.ch),
		Sending:      int(s.counters.sending.Load()),
		Held:         int(s.counters.held.Load()),
		Delivered:    s.counters.delivered.Load(),
		Handled:      s.counters.handled.Load(),
		Failed:       s.counters.failed.Load(),
		TimedOut:     s.counters.timedOut.Load(),
		Dropped:      s.counters.dropped.Load(),
		Merged:       s.counters.merged.Load(),
		Batches:      s.counters.batches.Load(),
		HandleTime:   time.Duration(s.counters.handleTime.Load()),
		Handling:     s.counters.handling(),
	}
}

// counters are what Stats reads, one method per event. They count values, n for a batch of n, each before the message
// is reported, so error handlers see theirs.
type counters struct {
	delivered  atomic.Uint64
	handled    atomic.Uint64
	failed     atomic.Uint64
	timedOut   atomic.Uint64
	dropped    atomic.Uint64
	merged     atomic.Uint64
	batches    atomic.Uint64
	handleTime atomic.Int64

	// batched counts the values of the batches in ch past their first, so that Queued counts values.
	batched atomic.Int64

	// held counts the messages in the holder.
	held atomic.Int64

	// sending counts each async send from before its goroutine starts until send returns.
	sending atomic.Int64

	// created is what handleStart counts from, on the monotonic clock.
	created time.Time

	// handleStart is when handle started on the current message, in nanoseconds since created, plus one so that 0
	// means idle even when no time has passed, as in a synctest bubble.
	handleStart atomic.Int64
}

// sent counts n values taken, or replayed. A batch's values past its first are counted in Queued until took.
func (c *counters) sent(n int) {
	if n > 1 {
		c.batched.Add(int64(n - 1))
	}
	c.delivered.Add(values(n))
}

// took uncounts from Queued the values past the first of n, just read from ch.
func (c *counters) took(n int) {
	if n > 1 {
		c.batched.Add(-int64(n - 1))
	}
}

// reserve counts an async send under way, unless limit are already. limit <= 0 means none.
func (c *counters) reserve(limit int) bool {
	if limit <= 0 {
		c.sending.Add(1)

		return true
	}
	for {
		n := c.sending.Load()
		if n >= int64(limit) {
			return false
		}
		if c.sending.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// unreserve uncounts an async send once done.
func (c *counters) unreserve() {
	c.sending.Add(-1)
}

// hold counts a message the holder took, merging away merged of those it held.
func (c *counters) hold(merged int) {
	merged = max(merged, 0)
	c.merged.Add(uint64(merged))
	c.held.Add(int64(1 - merged))
}

// unhold uncounts n values the holder released.
func (c *counters) unhold(n int) {
	c.held.Add(-int64(n))
}

// drop counts n values dropped.
func (c *counters) drop(n int) {
	c.dropped.Add(values(n))
}

// timeout counts n values timed out.
func (c *counters) timeout(n int) {
	c.timedOut.Add(values(n))
}

// start marks handle as running since now. It takes now, rather than reading the clock, so that it inlines, as stop does.
func (c *counters) start(now time.Time) {
	c.handleStart.Store(int64(now.Sub(c.created)) + 1)
}

// stop marks handle as idle, and counts n values it took d on, failed if err is not nil, as one call to the batch
// handler if batch.
func (c *counters) stop(d time.Duration, n int, batch bool, err error) {
	c.handleStart.Store(0)
	if batch {
		c.batches.Add(1)
	}
	c.handleTime.Add(int64(d))
	if err != nil {
		c.failed.Add(values(n))

		return
	}
	c.handled.Add(values(n))
}

// handling returns how long handle has been running on the current message, or 0 if idle.
func (c *counters) handling() time.Duration {
	start := c.handleStart.Load()
	if start == 0 {
		return 0
	}

	return time.Since(c.created) - time.Duration(start-1)
}

// values converts a message's value count to a counter's.
func values(n int) uint64 {
	return uint64(max(n, 0))
}
