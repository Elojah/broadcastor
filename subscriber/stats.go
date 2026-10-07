package subscriber

import (
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Stats is a snapshot of a subscriber's counters. Each message a Broadcast picks it up for, and each replayed value,
// ends up counted once in Handled, Failed, TimedOut or Dropped. Delivered is Handled + Failed + Queued, plus one while
// handle runs, but the counters are read one at a time, so they may not add up while messages are in flight. For
// SubscribeSeq, handle is the loop body.
type Stats struct {
	SubscriberID uuid.UUID

	// Queued messages wait in a buffer of size Buffer (WithBuffer).
	Queued int
	Buffer int
	// Sending counts the async sends under way (WithAsyncLimit).
	Sending int

	// Delivered counts the messages taken and the values replayed.
	Delivered uint64
	// Handled and Failed count the messages handle, or its outermost middleware, returned nil or an error for.
	Handled uint64
	Failed  uint64
	// TimedOut and Dropped count the messages lost as a *TimeoutError or a *DroppedError.
	TimedOut uint64
	Dropped  uint64

	// HandleTime is the time spent in handle and its middlewares, middleware.Retry's waits included.
	HandleTime time.Duration
	// Handling is how long handle has been running on the current message, 0 when idle: a watchdog can unsubscribe a
	// subscriber whose handle hangs.
	Handling time.Duration
}

// counters are what Stats reads. Each is counted before the message is reported, so error handlers see theirs.
type counters struct {
	delivered  atomic.Uint64
	handled    atomic.Uint64
	failed     atomic.Uint64
	timedOut   atomic.Uint64
	dropped    atomic.Uint64
	handleTime atomic.Int64

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

// handle counts a message handle took d on, and failed on if err is not nil.
func (c *counters) handle(d time.Duration, err error) {
	c.handleTime.Add(int64(d))
	if err != nil {
		c.failed.Add(1)

		return
	}
	c.handled.Add(1)
}
