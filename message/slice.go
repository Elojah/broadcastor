package message

import (
	"iter"
	"slices"
)

// Slice is a Batch of the values in a slice.
type Slice[T any] []T

// Add appends msg.
func (s *Slice[T]) Add(msg T) {
	*s = append(*s, msg)
}

// All yields each value, in order.
func (s *Slice[T]) All() iter.Seq[T] {
	return slices.Values(*s)
}

// Len returns how many values s holds.
func (s *Slice[T]) Len() int {
	return len(*s)
}

// New returns an empty Slice.
func (s *Slice[T]) New() Batch[T] {
	return new(Slice[T])
}
