package holder

import (
	"cmp"
	"time"

	"github.com/elojah/broadcastor/message"
)

// Jitter is a subscriber.Holder buffering jitter: it holds each message for d from when it came, and once one is due,
// releases it after every message held that is ordered before it. So each message reaches handle within d, and in
// order unless it came d or more after one ordered after it. Those that compare equal leave in the order they came.
// Unlike Reorder, it has no windows: a steady stream is sorted throughout. Each message keeps its own Config. Create
// one with NewJitter or NewJitterFunc.
type Jitter[T any] struct {
	d        time.Duration
	held     minHeap[T]
	arrivals arrivals
}

// NewJitter returns a Jitter holding each message for d, releasing them in ascending order. With d <= 0, each message
// is released at once.
func NewJitter[T cmp.Ordered](d time.Duration) *Jitter[T] {
	return NewJitterFunc(d, cmp.Compare[T])
}

// NewJitterFunc returns a Jitter holding each message for d, releasing them in ascending order by cmp, as
// slices.SortFunc sorts: swap a and b to release them in descending order. With d <= 0, each message is released at
// once.
func NewJitterFunc[T any](d time.Duration, cmp func(a, b T) int) *Jitter[T] {
	return &Jitter[T]{d: d, held: minHeap[T]{cmp: cmp}}
}

// Hold holds m until d after now. It merges none away.
func (j *Jitter[T]) Hold(m message.Message[T], now time.Time) (int, bool) {
	j.held.push(m, j.arrivals.add(now.Add(j.d)))

	return 0, true
}

// Release returns the first message held in order once the oldest one is due at now, or at once if all.
func (j *Jitter[T]) Release(now time.Time, all bool) (message.Message[T], time.Duration, bool) {
	due, ok := j.arrivals.oldest()
	if !ok {
		return message.Message[T]{}, 0, false
	}
	if wait := due.Sub(now); wait > 0 && !all {
		return message.Message[T]{}, wait, false
	}
	m, seq := j.held.pop()
	j.arrivals.release(seq)

	return m, 0, true
}

// minHeap holds messages by cmp, then by seq, so that those that compare equal leave in the order they came.
type minHeap[T any] struct {
	cmp   func(a, b T) int
	items []heapItem[T]
}

type heapItem[T any] struct {
	m   message.Message[T]
	seq uint64
}

func (h *minHeap[T]) push(m message.Message[T], seq uint64) {
	h.items = append(h.items, heapItem[T]{m: m, seq: seq})
	h.up(len(h.items) - 1)
}

// pop removes and returns the first message, with its seq.
func (h *minHeap[T]) pop() (message.Message[T], uint64) {
	first := h.items[0]
	last := len(h.items) - 1
	h.items[0] = h.items[last]
	// Zeroed for the GC, since items keeps its capacity.
	h.items[last] = heapItem[T]{}
	h.items = h.items[:last]
	h.down(0)

	return first.m, first.seq
}

func (h *minHeap[T]) up(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if !h.less(i, parent) {
			return
		}
		h.swap(i, parent)
		i = parent
	}
}

func (h *minHeap[T]) down(i int) {
	for {
		child := 2*i + 1
		if child >= len(h.items) {
			return
		}
		if right := child + 1; right < len(h.items) && h.less(right, child) {
			child = right
		}
		if !h.less(child, i) {
			return
		}
		h.swap(i, child)
		i = child
	}
}

func (h *minHeap[T]) less(i, j int) bool {
	if c := h.cmp(h.items[i].m.Value, h.items[j].m.Value); c != 0 {
		return c < 0
	}

	return h.items[i].seq < h.items[j].seq
}

func (h *minHeap[T]) swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
}

// arrivals queues when each message since the oldest held is due, in the order they came, marking those released,
// before they were due or not.
type arrivals struct {
	queue []arrival
	// head is the oldest held's index in queue.
	head int
	// next is the seq of the next message, queue's last being next-1.
	next uint64
}

type arrival struct {
	due      time.Time
	released bool
}

// add queues a message due at due, and returns its seq.
func (a *arrivals) add(due time.Time) uint64 {
	// Compacted rather than resliced, so that a steady stream reuses queue.
	if a.head > 0 && len(a.queue) == cap(a.queue) {
		n := copy(a.queue, a.queue[a.head:])
		a.queue, a.head = a.queue[:n], 0
	}
	a.queue = append(a.queue, arrival{due: due})
	a.next++

	return a.next - 1
}

// oldest returns when the oldest message held is due, or false if none is held.
func (a *arrivals) oldest() (time.Time, bool) {
	if a.head == len(a.queue) {
		return time.Time{}, false
	}

	return a.queue[a.head].due, true
}

// release marks the message with seq released, and moves head past those released.
func (a *arrivals) release(seq uint64) {
	a.queue[len(a.queue)-int(a.next-seq)].released = true //nolint:gosec // it came since the oldest held, fewer than len(queue) ago
	for a.head < len(a.queue) && a.queue[a.head].released {
		a.head++
	}
}
