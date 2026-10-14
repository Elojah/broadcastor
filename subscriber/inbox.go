package subscriber

import (
	"sync/atomic"

	"github.com/elojah/broadcastor/message"
)

// inbox is the subscriber's channel. It counts the subscription's reference, which stop drops, and one per send, which
// acquire takes and release drops. Whoever drops the last one closes ch, so never under a sender.
type inbox[T any] struct {
	ch chan message.Message[T]

	// done is closed by stop, so that no send waits on a subscriber that is gone.
	done chan struct{}

	refs atomic.Int64
}

// open makes ch and done, and takes the subscription's reference.
func (in *inbox[T]) open(buffer int) {
	in.ch = make(chan message.Message[T], buffer)
	in.done = make(chan struct{})
	in.refs.Store(1)
}

// stop drops the subscription's reference. Every send waiting on ch gives up, still holding its reference, so ch is
// not closed under it. Only Unsubscribe calls it, once.
func (in *inbox[T]) stop() {
	close(in.done)
	in.release()
}

// acquire takes a reference for a send, unless ch is already closed.
func (in *inbox[T]) acquire() bool {
	for {
		n := in.refs.Load()
		if n == 0 {
			return false
		}
		if in.refs.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// release drops a reference and closes ch if it was the last one.
func (in *inbox[T]) release() {
	if in.refs.Add(-1) == 0 {
		close(in.ch)
	}
}
