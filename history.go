package broadcastor

import (
	"context"
	"sync"
)

// sequencer numbers the messages given to the History, and tells a replaying subscriber once every one it should read
// back has been appended. Its mutex is never held while sending or calling user code, so a Broadcast waits on it only
// for another's counter, or for a Subscribe's Store.
type sequencer struct {
	mu sync.Mutex

	// last is the newest offset, counting from 1.
	last uint64
	// appending holds the offsets whose Append has not returned yet.
	appending map[uint64]struct{}
	// appended is closed once an Append returns, then replaced. nil until a subscriber waits on it.
	appended chan struct{}
}

func newSequencer() *sequencer {
	return &sequencer{appending: make(map[uint64]struct{})}
}

// next returns a new offset, which done must be called with once its Append returns.
func (q *sequencer) next() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.last++
	q.appending[q.last] = struct{}{}

	return q.last
}

// done records that the Append of offset has returned.
func (q *sequencer) done(offset uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()

	delete(q.appending, offset)
	if q.appended != nil {
		close(q.appended)
		q.appended = nil
	}
}

// join calls store with the newest offset, so that every message with a later one finds what store stored.
func (q *sequencer) join(store func(cutoff uint64)) {
	q.mu.Lock()
	defer q.mu.Unlock()

	store(q.last)
}

// wait waits until every Append up to cutoff has returned, or returns ctx.Err() once ctx is done.
func (q *sequencer) wait(ctx context.Context, cutoff uint64) error {
	for {
		appended := q.waiting(cutoff)
		if appended == nil {
			return nil
		}
		select {
		case <-appended:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// waiting returns nil if every Append up to cutoff has returned, or else a channel closed once another returns.
func (q *sequencer) waiting(cutoff uint64) <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()

	for offset := range q.appending {
		if offset <= cutoff {
			if q.appended == nil {
				q.appended = make(chan struct{})
			}

			return q.appended
		}
	}

	return nil
}
