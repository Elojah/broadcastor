package subscriber

import (
	"time"

	"github.com/elojah/broadcastor/message"
)

// Consume calls handle, wrapped in the middlewares, for each replayed value then each message until ch closes, and
// reports errors. It runs once, then sends its ID on each WithDone channel, then calls finished.
func (s *Subscriber[T]) Consume(handle Handler[T]) {
	defer s.finish()
	handle = chain(handle, s.config.middlewares...)
	if s.config.replay != nil {
		s.replay(handle)
	}
	if s.config.holder != nil {
		s.pace(handle)

		return
	}
	for m := range s.inbox.ch {
		s.counters.took(m.Len())
		if s.discarding.Load() {
			s.closed(m)

			continue
		}
		s.process(handle, m)
	}
}

// finish sends the ID on each WithDone channel in turn, each waiting for a receiver until its ctx is done, then calls
// finished, so that Shutdown returns after.
func (s *Subscriber[T]) finish() {
	for _, done := range s.config.done {
		// Tried first, since select picks at random: a ready receiver gets the ID even once ctx is done.
		select {
		case done.ch <- s.id:
			continue
		default:
		}
		select {
		case done.ch <- s.id:
		case <-done.ctx.Done():
		}
	}
	s.finished()
}

// replay processes each replayed value like a message, until the subscriber discards.
func (s *Subscriber[T]) replay(handle Handler[T]) {
	for value := range s.config.replay {
		if s.discarding.Load() {
			return
		}
		s.counters.sent(1)
		s.process(handle, message.New(value, s.config.defaults, message.Config{}))
	}
}

// process passes m to handle, or its batch to the batch handler, counts it, and reports its error. Without a batch
// handler, a batch goes to handle one value at a time, each one reported instead once the subscriber discards.
func (s *Subscriber[T]) process(handle Handler[T], m message.Message[T]) {
	if m.Batch != nil && s.config.batchHandler == nil {
		// Called rather than ranged over, which would allocate the loop's state too.
		m.Batch.All()(func(value T) bool {
			v := message.Message[T]{Value: value, Config: m.Config}
			if s.discarding.Load() {
				s.closed(v)

				return true
			}
			s.process(handle, v)

			return true
		})

		return
	}
	start := time.Now()
	s.counters.start(start)
	var err error
	if m.Batch == nil {
		err = handle(s.context(m), s.id, m.Value)
	} else {
		err = s.config.batchHandler(s.context(m), s.id, m.Batch)
	}
	s.counters.stop(time.Since(start), m.Len(), m.Batch != nil, err)
	// Reported outside the chain, so Recover never catches a panic in an error handler or evict.
	if err != nil {
		s.lose(m, err)
	}
}
