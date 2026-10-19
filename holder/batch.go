package holder

import (
	"time"

	"github.com/elojah/broadcastor/v2/message"
)

// Batch is a subscriber.Holder that gathers the messages of a window into one batch, in order, and releases it once the
// window ends, d after its first message, or as soon as it holds n, for the subscriber's batch handler
// (subscriber.WithBatchHandler). The batch carries the newest message's Config, so its ctx and error handler. Create
// one with NewBatch or NewBatchFunc.
type Batch[T any] struct {
	d        time.Duration
	n        int
	newBatch func() message.Batch[T]

	// batch is the window's, nil between windows. Each window has a batch of its own, which handle may keep.
	batch  message.Batch[T]
	due    time.Time
	config message.Config
}

// NewBatch returns a Batch releasing a *message.Slice once its window has lasted d or holds n messages. With d <= 0,
// each message is released at once, alone, and with n <= 0, a window holds any number.
func NewBatch[T any](d time.Duration, n int) *Batch[T] {
	return NewBatchFunc(d, n, func() message.Batch[T] { return new(message.Slice[T]) })
}

// NewBatchFunc is NewBatch, with each window's batch from newBatch, such as a type of the caller's own.
func NewBatchFunc[T any](d time.Duration, n int, newBatch func() message.Batch[T]) *Batch[T] {
	return &Batch[T]{d: d, n: n, newBatch: newBatch}
}

// Hold adds m to the window's batch, starting a window if none is, and makes it due at once if it holds n. It merges
// none away.
func (b *Batch[T]) Hold(m message.Message[T], now time.Time) (int, bool) {
	if b.batch == nil {
		b.batch, b.due = b.newBatch(), now.Add(b.d)
	}
	b.batch.Add(m.Value)
	b.config = m.Config
	if b.n > 0 && b.batch.Len() >= b.n {
		b.due = now
	}

	return 0, true
}

// Release returns the window's batch once the window has ended at now, or at once if all.
func (b *Batch[T]) Release(now time.Time, all bool) (message.Message[T], time.Duration, bool) {
	if b.batch == nil {
		return message.Message[T]{}, 0, false
	}
	if wait := b.due.Sub(now); wait > 0 && !all {
		return message.Message[T]{}, wait, false
	}
	m := message.Message[T]{Batch: b.batch, Config: b.config}
	b.batch, b.config = nil, message.Config{}

	return m, 0, true
}
