package message

import (
	"iter"
	"maps"

	"github.com/google/uuid"
)

// Map is a Batch of the values in a map, each under a random UUID, so it has no order. The zero Map is empty and ready
// to use.
type Map[T any] map[uuid.UUID]T

// Add puts msg under a new random UUID.
func (m *Map[T]) Add(msg T) {
	if *m == nil {
		*m = make(Map[T])
	}
	(*m)[uuid.New()] = msg
}

// All yields each value, in no particular order.
func (m *Map[T]) All() iter.Seq[T] {
	return maps.Values(*m)
}

// Len returns how many values m holds.
func (m *Map[T]) Len() int {
	return len(*m)
}

// New returns an empty Map.
func (m *Map[T]) New() Batch[T] {
	return new(Map[T])
}
