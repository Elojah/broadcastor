package subscriber

import (
	"cmp"
	"time"

	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/pkg/reorder"
)

// OrderPolicy is the order a subscriber handles its messages in, and how long it waits for them (WithOrder).
type OrderPolicy[T any] struct {
	// Compare orders messages, as in slices.SortFunc: ByTime and By make one from a field. nil means no ordering.
	Compare func(a, b T) int

	// Window is the longest a message is held, from when the subscriber takes it, for one that sorts before it to
	// arrive. 0 or less orders only the messages queued together, waiting for none.
	Window time.Duration

	// Limit is how many messages are held at most: past it, the first in order is handled before its window ends. 0 or
	// less means no limit.
	Limit int
}

// ByTime orders messages by the time at returns, earliest first.
func ByTime[T any](at func(T) time.Time) func(a, b T) int {
	return func(a, b T) int {
		return at(a).Compare(at(b))
	}
}

// By orders messages by the key returned, smallest first.
func By[T any, K cmp.Ordered](key func(T) K) func(a, b T) int {
	return func(a, b T) int {
		return cmp.Compare(key(a), key(b))
	}
}

// ordering holds the messages of a subscriber with WithOrder until they are due. Only the subscriber's reader uses it:
// Consume, or the Seq loop and then its discard.
type ordering[T any] struct {
	buffer *reorder.Buffer[message.Message[T]]
	timer  *time.Timer

	// closed is set once ch is closed: nothing more can arrive, so everything held is due.
	closed bool
}

func newOrdering[T any](policy OrderPolicy[T]) *ordering[T] {
	compare := policy.Compare

	return &ordering[T]{buffer: reorder.New(func(a, b message.Message[T]) int { return compare(a.Value, b.Value) }, policy.Window, policy.Limit)}
}

// wait returns a channel that fires when the next message held is due, or nil if none is held.
func (o *ordering[T]) wait() <-chan time.Time {
	end, ok := o.buffer.Deadline()
	if !ok {
		return nil
	}
	if o.timer == nil {
		o.timer = time.NewTimer(time.Until(end))
	} else {
		o.timer.Reset(time.Until(end))
	}

	return o.timer.C
}

// stop stops the timer, once the reader is done.
func (o *ordering[T]) stop() {
	if o.timer != nil {
		o.timer.Stop()
	}
}
