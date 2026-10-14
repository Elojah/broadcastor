package holder

import (
	"time"

	"github.com/elojah/broadcastor/message"
)

// Reduce is a subscriber.Holder that reduces the messages of a window as they come, then releases what is left, in
// order, once the window ends, d after its first message. Every message it releases carries the newest message's
// Config, so its ctx and error handler. Create one with NewReduce.
type Reduce[T any] struct {
	d      time.Duration
	reduce func(held []T, next T) []T

	held []T
	// next is the index of the next message to release. A window is released whole before Hold comes again, so Hold
	// sees it 0.
	next int
	due  time.Time
	// config is the newest message's.
	config message.Config
}

// NewReduce returns a Reduce whose windows last d. reduce returns what to hold once next comes: held to drop next,
// append(held, next) to keep it, or held changed in place, such as next replacing one with the same key. It may reuse
// held, and must not return more than len(held) + 1 messages. What it keeps is all a window holds. With d <= 0, each
// message is released at once, and reduce sees it alone.
func NewReduce[T any](d time.Duration, reduce func(held []T, next T) []T) *Reduce[T] {
	return &Reduce[T]{d: d, reduce: reduce}
}

// Hold reduces m into the messages held, starting a window if none is, and returns how many reduce merged away.
func (r *Reduce[T]) Hold(m message.Message[T], now time.Time) (int, bool) {
	if len(r.held) == 0 {
		r.due = now.Add(r.d)
	}
	n := len(r.held) + 1
	r.held = r.reduce(r.held, m.Value)
	r.config = m.Config

	return max(n-len(r.held), 0), true
}

// Release returns the oldest message held once the window has ended at now, or at once if all.
func (r *Reduce[T]) Release(now time.Time, all bool) (message.Message[T], time.Duration, bool) {
	if len(r.held) == 0 {
		return message.Message[T]{}, 0, false
	}
	if wait := r.due.Sub(now); wait > 0 && !all {
		return message.Message[T]{}, wait, false
	}
	m := message.Message[T]{Value: r.held[r.next], Config: r.config}
	// Zeroed for the GC, since held keeps its capacity for the next window.
	var zero T
	r.held[r.next] = zero
	r.next++
	if r.next == len(r.held) {
		r.held, r.next, r.config = r.held[:0], 0, message.Config{}
	}

	return m, 0, true
}
