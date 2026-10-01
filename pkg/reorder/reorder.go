// Package reorder provides Buffer, which holds values back for a while and releases them in order.
package reorder

import "time"

// Buffer holds values for up to a window, and releases them in order. When a value's window ends, it is due, and so is
// every value held that sorts at or before it, so that none waits longer than the window. Past the limit, the first
// value in order is released early. A value pushed once a value it sorts before was released is late, and is not held.
// Equal values keep the order they were pushed in. A Buffer is for one goroutine at a time. Create one with New.
type Buffer[E any] struct {
	compare func(a, b E) int
	window  time.Duration
	limit   int

	// held is a min-heap by compare, then by arrival.
	held     []entry[E]
	arrivals uint64

	// windows holds every value pushed with the end of its window, in the order pushed, from head.
	windows []window[E]
	head    int

	// due is the greatest value whose window ended, which every value held at or before is due with.
	due    E
	hasDue bool

	last     E
	released bool
}

type entry[E any] struct {
	value   E
	arrival uint64
}

type window[E any] struct {
	value E
	end   time.Time
}

// New returns an empty Buffer that orders values with compare, as in slices.SortFunc, holds each one for window at most,
// and holds limit values at most, 0 or less meaning no limit. With a window of 0, Pop releases right away whatever was
// pushed, in order.
func New[E any](compare func(a, b E) int, window time.Duration, limit int) *Buffer[E] {
	return &Buffer[E]{compare: compare, window: max(window, 0), limit: limit}
}

// Push holds v, pushed at now, or returns false if it is late.
func (b *Buffer[E]) Push(v E, now time.Time) bool {
	if b.released && b.compare(v, b.last) < 0 {
		return false
	}
	b.arrivals++
	b.held = append(b.held, entry[E]{value: v, arrival: b.arrivals})
	b.up(len(b.held) - 1)
	b.windows = append(b.windows, window[E]{value: v, end: now.Add(b.window)})

	return true
}

// Pop releases the first value in order if it is due at now, or if more than the limit are held.
func (b *Buffer[E]) Pop(now time.Time) (E, bool) { //nolint:ireturn // E is a type parameter, not an interface
	for b.head < len(b.windows) && !b.windows[b.head].end.After(now) {
		if v := b.windows[b.head].value; !b.hasDue || b.compare(v, b.due) > 0 {
			b.due, b.hasDue = v, true
		}
		b.windows[b.head] = window[E]{}
		b.head++
	}
	// Reuse the slots before head once they are half the slice, so that windows does not grow with every value.
	if b.head > len(b.windows)/2 {
		n := copy(b.windows, b.windows[b.head:])
		clear(b.windows[n:])
		b.windows, b.head = b.windows[:n], 0
	}

	if len(b.held) == 0 {
		var zero E

		return zero, false
	}
	if (b.limit > 0 && len(b.held) > b.limit) || (b.hasDue && b.compare(b.held[0].value, b.due) <= 0) {
		return b.Release()
	}

	var zero E

	return zero, false
}

// Release releases the first value in order, due or not.
func (b *Buffer[E]) Release() (E, bool) { //nolint:ireturn // E is a type parameter, not an interface
	if len(b.held) == 0 {
		var zero E

		return zero, false
	}
	v := b.held[0].value
	n := len(b.held) - 1
	b.held[0] = b.held[n]
	b.held[n] = entry[E]{}
	b.held = b.held[:n]
	if n > 0 {
		b.down(0)
	}
	b.last, b.released = v, true

	return v, true
}

// Deadline returns when the next window ends, if a value is held.
func (b *Buffer[E]) Deadline() (time.Time, bool) {
	// A value held has a window that has not ended, or Pop would release it.
	if len(b.held) == 0 || b.head == len(b.windows) {
		return time.Time{}, false
	}

	return b.windows[b.head].end, true
}

// Last returns the last value released, which a late value sorts before.
func (b *Buffer[E]) Last() (E, bool) { //nolint:ireturn // E is a type parameter, not an interface
	return b.last, b.released
}

// Len returns how many values are held.
func (b *Buffer[E]) Len() int {
	return len(b.held)
}

func (b *Buffer[E]) less(i, j int) bool {
	if c := b.compare(b.held[i].value, b.held[j].value); c != 0 {
		return c < 0
	}

	return b.held[i].arrival < b.held[j].arrival
}

func (b *Buffer[E]) up(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if !b.less(i, parent) {
			return
		}
		b.held[i], b.held[parent] = b.held[parent], b.held[i]
		i = parent
	}
}

func (b *Buffer[E]) down(i int) {
	for {
		smallest, left, right := i, 2*i+1, 2*i+2
		if left < len(b.held) && b.less(left, smallest) {
			smallest = left
		}
		if right < len(b.held) && b.less(right, smallest) {
			smallest = right
		}
		if smallest == i {
			return
		}
		b.held[i], b.held[smallest] = b.held[smallest], b.held[i]
		i = smallest
	}
}
