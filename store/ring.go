package store

import (
	"context"
	"strconv"
	"sync"

	"github.com/elojah/broadcastor/subscriber"
)

// Ring is an in-memory Queue of fixed size. Put never waits nor fails: when full, it drops the oldest entry, even one
// Next returned, and counts it (Dropped). Create one with NewRing.
type Ring[T any] struct {
	mu sync.Mutex

	// entries is circular: n entries from head.
	entries []Entry[T]
	head    int
	n       int

	seq     uint64 // ID of the newest entry
	dropped uint64

	// ready is closed while the ring holds an entry. A channel rather than a sync.Cond, so that Next can select on ctx.
	ready chan struct{}
}

// NewRing returns an empty Ring with room for size entries, at least 1.
func NewRing[T any](size int) *Ring[T] {
	return &Ring[T]{entries: make([]Entry[T], max(size, 1)), ready: make(chan struct{})}
}

// Put adds record, dropping the oldest entry if the ring is full. It always returns nil.
func (r *Ring[T]) Put(_ context.Context, record subscriber.Record[T]) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.n == len(r.entries) {
		r.removeOldest()
		r.dropped++
	}
	r.seq++
	r.entries[(r.head+r.n)%len(r.entries)] = Entry[T]{Record: record, ID: strconv.FormatUint(r.seq, 10)}
	r.n++
	if r.n == 1 {
		close(r.ready)
	}

	return nil
}

// Next returns the oldest entry, waiting for one, or ctx.Err() once ctx is done.
func (r *Ring[T]) Next(ctx context.Context) (Entry[T], error) {
	for {
		if err := ctx.Err(); err != nil {
			return Entry[T]{}, err
		}
		entry, ready, ok := r.oldest()
		if ok {
			return entry, nil
		}
		select {
		case <-ready:
		case <-ctx.Done():
		}
	}
}

// Ack removes the entry with the given ID, if the ring holds it. It always returns nil.
func (r *Ring[T]) Ack(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Next only returns the oldest entry, so no other is worth checking.
	if r.n > 0 && r.entries[r.head].ID == id {
		r.removeOldest()
	}

	return nil
}

// Len returns how many entries the ring holds.
func (r *Ring[T]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.n
}

// Dropped returns how many entries Put dropped for lack of room.
func (r *Ring[T]) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.dropped
}

// oldest returns the oldest entry, or false and a channel closed once there is one.
func (r *Ring[T]) oldest() (Entry[T], <-chan struct{}, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.n == 0 {
		return Entry[T]{}, r.ready, false
	}

	return r.entries[r.head], nil, true
}

// removeOldest removes the oldest entry, which must exist, zeroing its slot so that its message can be collected.
func (r *Ring[T]) removeOldest() {
	r.entries[r.head] = Entry[T]{}
	r.head = (r.head + 1) % len(r.entries)
	r.n--
	if r.n == 0 {
		r.ready = make(chan struct{})
	}
}
