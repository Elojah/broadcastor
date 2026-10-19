package holder

import (
	"cmp"
	"slices"
	"time"

	"github.com/elojah/broadcastor/v2/message"
)

// Reorder is a subscriber.Holder that holds every message of a window, then releases them sorted once the window
// ends, d after its first message, those that compare equal in the order they came. Each one keeps its own Config.
// Create one with NewReorder or NewReorderFunc.
type Reorder[T any] struct {
	d   time.Duration
	cmp func(a, b T) int

	held []message.Message[T]
	// next is the index of the next message to release, held being sorted once it is above 0. A window is released
	// whole before Hold comes again, so Hold sees it 0.
	next int
	due  time.Time
}

// NewReorder returns a Reorder whose windows last d, releasing messages in ascending order. With d <= 0, each message
// is released at once.
func NewReorder[T cmp.Ordered](d time.Duration) *Reorder[T] {
	return NewReorderFunc(d, cmp.Compare[T])
}

// NewReorderFunc returns a Reorder whose windows last d, releasing messages in ascending order by cmp, as
// slices.SortFunc sorts: swap a and b to release them in descending order. With d <= 0, each message is released at
// once.
func NewReorderFunc[T any](d time.Duration, cmp func(a, b T) int) *Reorder[T] {
	return &Reorder[T]{d: d, cmp: cmp}
}

// Hold holds m, starting a window if none is. It merges none away.
func (r *Reorder[T]) Hold(m message.Message[T], now time.Time) (int, bool) {
	if len(r.held) == 0 {
		r.due = now.Add(r.d)
	}
	r.held = append(r.held, m)

	return 0, true
}

// Release returns the first message held in order once the window has ended at now, or at once if all.
func (r *Reorder[T]) Release(now time.Time, all bool) (message.Message[T], time.Duration, bool) {
	if len(r.held) == 0 {
		return message.Message[T]{}, 0, false
	}
	if wait := r.due.Sub(now); wait > 0 && !all {
		return message.Message[T]{}, wait, false
	}
	if r.next == 0 {
		slices.SortStableFunc(r.held, func(a, b message.Message[T]) int {
			return r.cmp(a.Value, b.Value)
		})
	}
	m := r.held[r.next]
	// Zeroed for the GC, since held keeps its capacity for the next window.
	r.held[r.next] = message.Message[T]{}
	r.next++
	if r.next == len(r.held) {
		r.held, r.next = r.held[:0], 0
	}

	return m, 0, true
}
