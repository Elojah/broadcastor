package broadcastor

import (
	"context"
	"runtime/debug"
	"sync/atomic"

	"github.com/google/uuid"
)

type subscriber[T any] struct {
	id     uuid.UUID
	ch     chan message[T]
	config subscriberConfig

	// discarding is set when the subscriber is unsubscribed with WithUnsubscribeDiscard, before ch is released: from then
	// on, consume and pull report every message they take instead of processing it.
	discarding atomic.Bool

	// autoUnsubscribe undoes the context.AfterFunc that add set up on the ctx passed to Subscribe or SubscribeSeq, once the
	// subscriber was removed another way. It is nil without autoUnsubscribe.
	autoUnsubscribe func() bool

	// refs is 1 for the subscription itself, plus 1 for each Broadcast currently sending on ch. Whoever drops it to 0
	// closes ch, so ch is never closed while a Broadcast can still send on it.
	refs atomic.Int64
}

// subscriberConfig is what the subscriber options passed to Subscribe or SubscribeSeq set, which add then builds the
// subscriber from. Subscriber options change nothing else.
type subscriberConfig struct {
	// buffer is the size of the subscriber's channel buffer, 0 for unbuffered.
	buffer int

	// defaults is what every message sent to this subscriber starts from, before the Broadcast's own options.
	defaults messageConfig

	// unsubscribeDefaults is what every Unsubscribe and Close of this subscriber starts from, before the Unsubscribe's
	// own options.
	unsubscribeDefaults unsubscription

	// errorHandler is called with every error handle returns, or nil to discard them.
	errorHandler func(ctx context.Context, err error)

	// recover makes a panic in handle a *PanicError instead of a crash.
	recover bool

	// autoUnsubscribe makes add remove the subscriber once the ctx passed to Subscribe or SubscribeSeq is done.
	autoUnsubscribe bool
}

// send hands m to the subscriber and reports whether it took it. It gives up once ctx is done or m's timeout runs out,
// or right away for a non-blocking message, then drops the reference the caller acquired.
func (s *subscriber[T]) send(ctx context.Context, m message[T]) bool {
	defer s.release()

	if m.config.delivery == deliveryNonBlocking {
		select {
		case s.ch <- m:
			return true
		default:
			s.report(ctx, m, &DroppedError[T]{SubscriberID: s.id, Message: m.value})

			return false
		}
	}

	sendCtx := ctx
	if m.config.timeout > 0 {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(ctx, m.config.timeout)
		defer cancel()
	}

	select {
	case s.ch <- m:
		return true
	case <-sendCtx.Done():
		// ctx, not sendCtx, so that the error handler is not handed a ctx that the timeout alone has ended.
		s.report(ctx, m, &TimeoutError[T]{SubscriberID: s.id, Message: m.value, Err: sendCtx.Err()})

		return false
	}
}

// acquire takes a reference for a Broadcast, unless ch is already closed.
func (s *subscriber[T]) acquire() bool {
	for {
		n := s.refs.Load()
		if n == 0 {
			return false
		}
		if s.refs.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// release drops a reference and closes ch if it was the last one.
func (s *subscriber[T]) release() {
	if s.refs.Add(-1) == 0 {
		close(s.ch)
	}
}

// unsubscribe drops the subscription's reference, and first makes the subscriber discard if its unsubscribe defaults,
// then options, say so. It must be called once, by whoever took the subscriber out of the map.
func (s *subscriber[T]) unsubscribe(options ...UnsubscribeOption) {
	u := s.config.unsubscribeDefaults
	for _, option := range options {
		option(&u)
	}
	// Before release, which may close ch right away, so that the subscriber discards everything left in its buffer.
	if u.discard {
		s.discarding.Store(true)
	}
	// However it was removed, so that nothing keeps waiting for a ctx that may never be done.
	if s.autoUnsubscribe != nil {
		s.autoUnsubscribe()
	}

	// Closes the channel now, or once the last Broadcast still sending on it is done.
	s.release()
}

// report passes err to the subscriber's error handler, then to m's, skipping whichever is not set.
func (s *subscriber[T]) report(ctx context.Context, m message[T], err error) {
	if s.config.errorHandler != nil {
		s.config.errorHandler(ctx, err)
	}
	if m.config.errorHandler != nil {
		m.config.errorHandler(ctx, err)
	}
}

/*
	Subscribe with iterator methods
	#MARK: Iterator methods
*/

// pull is consume for SubscribeSeq: it yields every message the subscriber takes, one at a time, until yield returns
// false, ctx is done, ch is closed or the subscriber is discarding. A message it takes once the subscriber is
// discarding is reported, and the caller's discard reports the rest.
func (s *subscriber[T]) pull(ctx context.Context, yield func(T) bool) {
	for ctx.Err() == nil && !s.discarding.Load() {
		select {
		case m, ok := <-s.ch:
			if !ok {
				return
			}
			if s.discarding.Load() {
				s.report(ctx, m, &SubscriberClosedError[T]{SubscriberID: s.id, Message: m.value})

				return
			}
			if !yield(m.value) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// discard reads ch until it is closed, once a SubscribeSeq loop has ended and unsubscribed, so that no Broadcast still
// sending to it waits for nothing. Every message it reads was taken but never yielded, and is reported as such.
func (s *subscriber[T]) discard(ctx context.Context) {
	for m := range s.ch {
		s.report(ctx, m, &SubscriberClosedError[T]{SubscriberID: s.id, Message: m.value})
	}
}

/*
	Subscribe with callback methods
	#MARK: Callback methods
*/

func (s *subscriber[T]) consume(ctx context.Context, handle func(ctx context.Context, msg T) error) {
	for m := range s.ch {
		if s.discarding.Load() {
			s.report(ctx, m, &SubscriberClosedError[T]{SubscriberID: s.id, Message: m.value})

			continue
		}
		// Reported here rather than from process, so that a panic in an error handler is not recovered.
		if err := s.process(ctx, handle, m.value); err != nil {
			s.report(ctx, m, err)
		}
	}
}

// process calls handle and returns a *HandleError if it fails, or a *PanicError if it panics and the subscriber
// recovers.
func (s *subscriber[T]) process(ctx context.Context, handle func(ctx context.Context, msg T) error, value T) (err error) {
	if s.config.recover {
		defer func() {
			if v := recover(); v != nil {
				err = &PanicError[T]{SubscriberID: s.id, Message: value, Value: v, Stack: debug.Stack()}
			}
		}()
	}

	if handleErr := handle(ctx, value); handleErr != nil {
		return &HandleError[T]{SubscriberID: s.id, Message: value, Err: handleErr}
	}

	return nil
}
