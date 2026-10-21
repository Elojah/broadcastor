package message

import "iter"

// Batch holds values that PublishBatch hands to each subscriber at once, with one Config. Slice and Map are two.
// Every subscriber reads the same batch, possibly at once, so All and Len must be safe for concurrent use.
type Batch[T any] interface {
	// Add adds msg.
	Add(msg T)
	// All yields each value, in the batch's order if it has one.
	All() iter.Seq[T]
	// Len returns how many values All yields. It is called often, so it must be cheap.
	Len() int
	// New returns an empty batch of the same kind, never nil, which a subscriber's filter fills with what it keeps.
	New() Batch[T]
}
