package subscriber

import (
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Stats is a snapshot of a subscriber's counters since it subscribed. Each message a Broadcast picks the subscriber up
// for is counted once in Handled, Failed, TimedOut or Dropped, or is still on its way, and so is each value it replays
// (WithReplay), in Delivered then Handled or Failed. So Delivered is Handled + Failed + Queued, plus the message in
// handle if there is one. The counters are read one at a time, so while messages are in flight they may not add up.
// For SubscribeSeq, handle is the loop body, and its error the one it passes to fail.
type Stats struct {
	SubscriberID uuid.UUID

	// Queued is how many messages wait in the subscriber's buffer, which has room for Buffer (WithBuffer).
	Queued int
	Buffer int
	// Sending is how many async sends (message.WithAsync) are under way to the subscriber, which WithAsyncLimit
	// bounds.
	Sending int

	// Delivered counts the messages the subscriber took, and the values it replayed.
	Delivered uint64
	// Handled counts the messages handle, or its outermost middleware, returned nil for.
	Handled uint64
	// Failed counts the messages handle, or its outermost middleware, returned an error for.
	Failed uint64
	// TimedOut counts the messages reported as a *TimeoutError.
	TimedOut uint64
	// Dropped counts the messages reported as a *DroppedError.
	Dropped uint64

	// HandleTime is the time spent in handle and its middlewares, middleware.Retry's waits included, for Handled +
	// Failed messages.
	HandleTime time.Duration
	// Handling is how long handle and its middlewares have been running on the current message, 0 when idle. A
	// watchdog can unsubscribe a subscriber whose handle hangs.
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

	// sending counts each async send from before its goroutine starts until send has returned.
	sending atomic.Int64

	// handleStart is when handle started on the current message, in nanoseconds since the subscriber was created, plus
	// one so that 0 means idle even when no time has passed, as in a synctest bubble.
	handleStart atomic.Int64
}

// handling returns how long handle has been running on the current message, or 0 if it is idle. created is when the
// subscriber was created.
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
