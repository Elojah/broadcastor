package subscriber

import (
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Stats is a snapshot of a subscriber's counters since it subscribed. Each message a Broadcast picks the subscriber up
// for is counted once in Handled, Failed, TimedOut, Dropped or Late, or is still on its way, so Delivered is Handled +
// Failed + Late + Queued + Held, plus the message in handle if there is one. The counters are read one at a time, so
// while messages are in flight they may not add up.
type Stats struct {
	SubscriberID uuid.UUID

	// Queued is how many messages wait in the subscriber's buffer, which has room for Buffer (WithBuffer).
	Queued int
	Buffer int
	// Held is how many messages the subscriber holds until they are due (WithOrder).
	Held int

	// Delivered counts the messages the subscriber took.
	Delivered uint64
	// Handled counts the messages handle returned nil for, or a SubscribeSeq loop body got.
	Handled uint64
	// Failed counts the messages handle, or its outermost middleware, returned an error for.
	Failed uint64
	// TimedOut counts the messages reported as a *TimeoutError.
	TimedOut uint64
	// Dropped counts the messages reported as a *DroppedError.
	Dropped uint64
	// Late counts the messages reported as a *LateError.
	Late uint64

	// HandleTime is the time spent in handle and its middlewares, middleware.Retry's waits included, or in the loop
	// body, for Handled + Failed messages.
	HandleTime time.Duration
}

// counters are what Stats reads. Each is counted before the message is reported, so error handlers see theirs.
type counters struct {
	delivered  atomic.Uint64
	handled    atomic.Uint64
	failed     atomic.Uint64
	timedOut   atomic.Uint64
	dropped    atomic.Uint64
	late       atomic.Uint64
	held       atomic.Int64
	handleTime atomic.Int64
}

// handle counts a message handle or the loop body took d on, and failed on if err is not nil.
func (c *counters) handle(d time.Duration, err error) {
	c.handleTime.Add(int64(d))
	if err != nil {
		c.failed.Add(1)

		return
	}
	c.handled.Add(1)
}
