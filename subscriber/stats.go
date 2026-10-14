package subscriber

import (
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/message"
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

// counters are what Stats reads. Each is counted before the message is reported, so error handlers see theirs.
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

	// handleStart is when handle started on the current message, in nanoseconds since created, plus one so that 0
	// means idle even when no time has passed, as in a synctest bubble.
	handleStart atomic.Int64
}

// handling returns how long handle has been running on the current message, or 0 if idle.
func (c *counters) handling(created time.Time) time.Duration {
	start := c.handleStart.Load()
	if start == 0 {
		return 0
	}

	return time.Since(created) - time.Duration(start-1)
}

// handle counts n messages handle took d on, and failed on if err is not nil.
func (c *counters) handle(d time.Duration, n uint64, err error) {
	c.handleTime.Add(int64(d))
	if err != nil {
		c.failed.Add(n)

		return
	}
	c.handled.Add(n)
}

// count returns how many messages m counts as: 1, or its batch's values.
func count[T any](m message.Message[T]) uint64 {
	return uint64(max(m.Len(), 0))
}
