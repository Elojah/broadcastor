package holder

import "time"

// NewLatest returns a Reduce that keeps only the newest message of a window: of the messages a window takes, only the
// last reaches handle, with its own Config. With d <= 0, each message is released at once.
func NewLatest[T any](d time.Duration) *Reduce[T] {
	return NewReduce(d, func(held []T, next T) []T {
		return append(held[:0], next)
	})
}
