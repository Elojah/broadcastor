package subscriber

import (
	"context"

	"github.com/elojah/multicast/message"
)

// Deliver sends value, with config laid over the subscriber's defaults, and reports whether it was taken, or for async,
// whether a send started. A parallel send returns false and a channel yielding that instead. A value the filter
// rejects returns false and does nothing else. ctx only bounds the wait.
func (s *Subscriber[T]) Deliver(ctx context.Context, value T, config message.Config) (bool, <-chan bool) {
	if s.config.filter != nil && !s.config.filter(value) {
		return false, nil
	}

	return s.deliver(ctx, message.New(value, s.config.defaults, config))
}

// DeliverBatch is Deliver for a batch, which must not be empty. Of a batch the filter keeps part of, it sends a batch
// from batch.New holding what it keeps.
func (s *Subscriber[T]) DeliverBatch(ctx context.Context, batch message.Batch[T], config message.Config) (bool, <-chan bool) {
	if s.config.filter != nil {
		if batch = s.keep(batch); batch == nil {
			return false, nil
		}
	}

	return s.deliver(ctx, message.NewBatch(batch, s.config.defaults, config))
}

// deliver sends m as its delivery says, for Deliver and DeliverBatch.
func (s *Subscriber[T]) deliver(ctx context.Context, m message.Message[T]) (bool, <-chan bool) {
	if !s.inbox.acquire() {
		s.closed(m) //nolint:contextcheck // reported with the message's ctx

		return false, nil
	}

	if m.Config.Delivery == message.DeliveryAsync {
		if !s.counters.reserve(s.config.asyncLimit) {
			defer s.inbox.release()
			s.drop(m) //nolint:contextcheck // reported with the message's ctx

			return false, nil
		}
		go func() {
			s.send(ctx, m)
			s.counters.unreserve()
		}()

		return true, nil
	}

	if m.Config.Delivery == message.DeliveryParallel {
		// Buffered, so the goroutine never waits for the reader.
		result := make(chan bool, 1)
		go func() { result <- s.send(ctx, m) }()

		return false, result
	}

	return s.send(ctx, m), nil
}

// send hands m over and releases the caller's reference. It gives up once ctx is done, m's timeout runs out or the
// subscriber is unsubscribed, or right away if m is non-blocking.
func (s *Subscriber[T]) send(ctx context.Context, m message.Message[T]) bool {
	// Once m is reported, so that an eviction is reported before ch can close.
	defer s.inbox.release()

	// Checked first, since select picks at random: once unsubscribed, only a send racing Unsubscribe gets through.
	select {
	case <-s.inbox.done:
		s.closed(m) //nolint:contextcheck // reported with the message's ctx

		return false
	default:
	}

	// Tried first, so that a ready subscriber costs neither a timer nor the select's locks, and takes m even if ctx is
	// done.
	select {
	case s.inbox.ch <- m:
		s.counters.sent(m.Len())

		return true
	default:
	}

	if m.Config.Delivery == message.DeliveryNonBlocking {
		s.drop(m) //nolint:contextcheck // reported with the message's ctx

		return false
	}

	sendCtx := ctx
	if m.Config.Timeout > 0 {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(ctx, m.Config.Timeout)
		defer cancel()
	}

	select {
	case s.inbox.ch <- m:
		s.counters.sent(m.Len())

		return true
	case <-s.inbox.done:
		s.closed(m) //nolint:contextcheck // reported with the message's ctx

		return false
	case <-sendCtx.Done():
		s.timeout(m, sendCtx.Err())

		return false
	}
}

// keep returns what of batch the filter keeps: batch itself if every value, nil if none, else a batch from batch.New.
// The filter sees each value once, since it may keep state.
func (s *Subscriber[T]) keep(batch message.Batch[T]) message.Batch[T] {
	kept := batch.New()
	for value := range batch.All() {
		if s.config.filter(value) {
			kept.Add(value)
		}
	}
	switch kept.Len() {
	case 0:
		return nil
	case batch.Len():
		return batch
	default:
		return kept
	}
}
