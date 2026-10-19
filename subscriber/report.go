package subscriber

import (
	"context"
	"errors"

	"github.com/elojah/broadcastor/v2/message"
)

// lose reports m, lost or failed with err, and evicts the subscriber if evict picks err. Only the call whose remove
// succeeds reports an *EvictedError, so a subscriber is evicted once, and never once removed. A *ClosedError never
// reaches evict: an ended Seq loop's would race its own removal.
func (s *Subscriber[T]) lose(m message.Message[T], err error) {
	if s.config.evict == nil || errors.Is(err, ErrClosed) || !s.config.evict(err) || !s.remove(WithUnsubscribeDiscard()) {
		s.report(m, err)

		return
	}
	s.report(m, &EvictedError[T]{SubscriberID: s.id, Message: m.Value, Batch: m.Batch, Err: err})
}

// closed reports m, missed because the subscriber was unsubscribed, as a *ClosedError.
func (s *Subscriber[T]) closed(m message.Message[T]) {
	s.report(m, &ClosedError[T]{SubscriberID: s.id, Message: m.Value, Batch: m.Batch})
}

// drop counts m as dropped and loses it as a *DroppedError.
func (s *Subscriber[T]) drop(m message.Message[T]) {
	s.counters.drop(m.Len())
	s.lose(m, &DroppedError[T]{SubscriberID: s.id, Message: m.Value, Batch: m.Batch})
}

// timeout counts m as timed out and loses it as a *TimeoutError wrapping err.
func (s *Subscriber[T]) timeout(m message.Message[T], err error) {
	s.counters.timeout(m.Len())
	s.lose(m, &TimeoutError[T]{SubscriberID: s.id, Message: m.Value, Batch: m.Batch, Err: err})
}

// report stores m, a batch one value at a time, then passes err to the subscriber's error handler, then to m's, once
// even for a batch. A failed Put turns err into a *StoreError.
func (s *Subscriber[T]) report(m message.Message[T], err error) {
	ctx := s.context(m)
	if s.config.store != nil {
		if putErr := s.put(ctx, m, err); putErr != nil {
			err = &StoreError[T]{SubscriberID: s.id, Message: m.Value, Batch: m.Batch, Err: putErr, Cause: err}
		}
	}
	if s.config.errorHandler != nil {
		s.config.errorHandler(ctx, err)
	}
	if m.Config.ErrorHandler != nil {
		m.Config.ErrorHandler(ctx, err)
	}
}

// put stores m, lost with err, or each value of its batch, and returns the errors of the Puts that failed.
func (s *Subscriber[T]) put(ctx context.Context, m message.Message[T], err error) error {
	// Never done: ctx being done is often why m was lost.
	ctx = context.WithoutCancel(ctx)
	if m.Batch == nil {
		return s.config.store.Put(ctx, Record[T]{SubscriberID: s.id, Message: m.Value, Err: err})
	}
	var errs []error
	for value := range m.Batch.All() {
		if putErr := s.config.store.Put(ctx, Record[T]{SubscriberID: s.id, Message: value, Err: err}); putErr != nil {
			errs = append(errs, putErr)
		}
	}

	return errors.Join(errs...)
}

// context returns m's own ctx, or else the subscriber's.
func (s *Subscriber[T]) context(m message.Message[T]) context.Context {
	if m.Config.Context != nil {
		return m.Config.Context
	}

	return s.ctx
}
