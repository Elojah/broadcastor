package store

import (
	"cmp"
	"context"
	"slices"
	"sync"

	"github.com/elojah/broadcastor/subscriber"
)

// History is an in-memory subscriber.History that keeps the last messages broadcast, for broadcastor.WithHistory.
// Append drops the one appended first once full. Create one with NewHistory.
type History[T any] struct {
	mu sync.Mutex

	// entries is circular: n entries from head, in the order appended.
	entries []subscriber.HistoryEntry[T]
	head, n int
}

// NewHistory returns an empty History with room for size messages, at least 1.
func NewHistory[T any](size int) *History[T] {
	return &History[T]{entries: make([]subscriber.HistoryEntry[T], max(size, 1))}
}

// Append keeps msg under offset, dropping the entry appended first if the history is full.
func (h *History[T]) Append(_ context.Context, offset uint64, msg T) {
	h.mu.Lock()
	defer h.mu.Unlock()

	entry := subscriber.HistoryEntry[T]{Offset: offset, Message: msg}
	if h.n == len(h.entries) {
		h.entries[h.head] = entry
		h.head = (h.head + 1) % len(h.entries)
	} else {
		h.entries[(h.head+h.n)%len(h.entries)] = entry
		h.n++
	}
}

// Read returns a copy of the messages kept, by offset. It always returns a nil error.
func (h *History[T]) Read(_ context.Context) ([]subscriber.HistoryEntry[T], error) {
	h.mu.Lock()
	entries := make([]subscriber.HistoryEntry[T], h.n)
	for i := range entries {
		entries[i] = h.entries[(h.head+i)%len(h.entries)]
	}
	h.mu.Unlock()

	// Concurrent Broadcasts may append out of order, though seldom far.
	slices.SortFunc(entries, func(a, b subscriber.HistoryEntry[T]) int { return cmp.Compare(a.Offset, b.Offset) })

	return entries, nil
}

// Len returns how many messages the history keeps.
func (h *History[T]) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.n
}
