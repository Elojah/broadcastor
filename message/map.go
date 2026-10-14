package message

import (
	"iter"
	"maps"
	"sync/atomic"
)

// mapKeys is the last key Map.Add put a value under, shared by every Map so that keys never collide.
var mapKeys atomic.Uint64

// Map is a Batch of the values in a map, each under its own key, so it has no order. The zero Map is empty and ready
// to use.
type Map[T any] map[uint64]T

// Add puts msg under a new key.
func (m *Map[T]) Add(msg T) {
	if *m == nil {
		*m = make(Map[T])
	}
	(*m)[mapKeys.Add(1)] = msg
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
