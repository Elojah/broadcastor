// Package limit provides Semaphore, which bounds how many goroutines run at once.
package limit

import "context"

// Semaphore hands out n slots.
type Semaphore struct {
	slots chan struct{}
}

// NewSemaphore returns a Semaphore with n slots.
func NewSemaphore(n int) *Semaphore {
	return &Semaphore{slots: make(chan struct{}, n)}
}

// Acquire takes a slot, waiting for one to be released until ctx is done, in which case it returns ctx's error. A free
// slot wins over a done ctx.
func (s *Semaphore) Acquire(ctx context.Context) error {
	select {
	case s.slots <- struct{}{}:
		return nil
	default:
	}
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees a slot that Acquire took.
func (s *Semaphore) Release() {
	<-s.slots
}
