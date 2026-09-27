package broadcastor

import (
	"context"
	"iter"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

// Broadcastor hands every message passed to Broadcast to each of its subscribers. Its zero value is not usable: create
// one with NewBroadcastor. It is safe for concurrent use, including from within a subscriber's handle.
type Broadcastor[T any] struct {
	subscribers sync.Map // [uuid.UUID]*subscriber[T]

	// closed is set by Close, before it unsubscribes everyone.
	closed atomic.Bool
}

// NewBroadcastor returns a Broadcastor with no subscribers.
func NewBroadcastor[T any]() *Broadcastor[T] {
	return &Broadcastor[T]{}
}

// Subscribe adds a subscriber and returns its ID, for Unsubscribe. The subscriber gets its own goroutine, which calls
// handle for every message it is sent, one at a time and in the order it takes them, until it is unsubscribed and has
// processed everything it took. handle is given a ctx derived from ctx, which Close uses to tell it is called from
// handle. Cancelling ctx does not unsubscribe the subscriber. Subscribe fails with ErrClosed once Close has been
// called, and otherwise only if it cannot generate the ID. A Subscribe running at the same time as Close may instead
// succeed, and then its subscriber is unsubscribed right away.
func (b *Broadcastor[T]) Subscribe(ctx context.Context, handle func(ctx context.Context, msg T) error, options ...SubscriberOption[T]) (uuid.UUID, error) {
	s, err := b.subscribe(ctx, handle, options)
	if err != nil {
		return uuid.Nil, err
	}

	return s.id, nil
}

// SubscribeSeq adds a subscriber like Subscribe, but hands its messages to a range loop instead of a handle function:
//
//	id, msgs, err := b.SubscribeSeq(ctx)
//	...
//	for msg := range msgs {
//		...
//	}
//
// The loop body plays the part of handle, in the caller's goroutine: the subscriber takes its next message once the
// body is done with the last one. Breaking out of the loop, returning or panicking from it, or ctx being done ends the
// loop and unsubscribes the subscriber, and the messages it had taken but not yielded yet are dropped. Unsubscribe and
// Close end the loop too, but only once it has yielded those messages, as a subscriber from Subscribe would process
// them.
//
// The subscription starts right away, not when the loop does, so that no message is missed in between: until then, the
// subscriber holds Broadcast up like a busy one. msgs can be ranged over once only, and yields nothing after that. A
// subscription that is never ranged over has to be unsubscribed.
//
// The options are those of Subscribe, but WithSubscriberRecover does nothing: a panic in the loop body goes up through
// the range statement like any other. With no handle to fail, the error handlers only get Broadcast's errors.
func (b *Broadcastor[T]) SubscribeSeq(ctx context.Context, options ...SubscriberOption[T]) (uuid.UUID, iter.Seq[T], error) {
	s, err := b.subscribe(ctx, nil, options)
	if err != nil {
		return uuid.Nil, nil, err
	}

	var ranged atomic.Bool

	return s.id, func(yield func(T) bool) {
		if ranged.Swap(true) {
			return
		}
		defer b.leave(s)

		for {
			select {
			case m, ok := <-s.ch:
				// ctx is checked again, so that nothing is yielded once it is done, whichever case select picked.
				if !ok || ctx.Err() != nil || !yield(m.value) {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}, nil
}

// Unsubscribe removes the subscriber with the given ID, or returns a *SubscriberNotFoundError if there is none. It
// never waits: the subscriber may still get messages after Unsubscribe returns, from its buffer or from a Broadcast
// that was already sending to it, and its goroutine ends once it has processed them. So it is safe to call from the
// subscriber's own handle. ctx is unused.
func (b *Broadcastor[T]) Unsubscribe(ctx context.Context, id uuid.UUID) error {
	if b.remove(id) == nil {
		return &SubscriberNotFoundError{SubscriberID: id}
	}

	return nil
}

// Broadcast hands msg to every subscriber, and returns how many it handed it to: the subscribers that took it, plus
// those that an async send was started for, which may still miss it. Once Close has been called, it hands msg to
// nobody and returns ErrClosed, which is the only error it returns.
//
// By default, Broadcast goes through the subscribers one at a time, and waits for each to take the message. A
// subscriber only takes its next message once handle has returned, unless it has room in its buffer, so a slow
// subscriber holds up Broadcast and every subscriber after it. Broadcast stops waiting once ctx is done, or once the
// message's timeout runs out, counted separately for each subscriber: that subscriber misses the message, and so may
// every later one once ctx is done. WithMessageAsync and WithMessageNonBlocking change how Broadcast waits.
//
// Every subscriber that misses the message has its error handlers given the reason, with ctx: a *TimeoutError, a
// *DroppedError, or a *SubscriberClosedError when it was unsubscribed since Broadcast picked it up.
func (b *Broadcastor[T]) Broadcast(ctx context.Context, msg T, options ...MessageOptions[T]) (int, error) {
	if b.closed.Load() {
		return 0, ErrClosed
	}

	var n int
	b.subscribers.Range(func(_, value any) bool {
		s, ok := value.(*subscriber[T])
		if !ok {
			return true
		}

		// The subscriber's defaults first, so that the Broadcast's own options override them.
		m := s.defaults
		m.value = msg
		for _, option := range options {
			option(&m)
		}

		if !s.acquire() {
			s.report(ctx, m, &SubscriberClosedError[T]{SubscriberID: s.id, Message: msg})

			return true // unsubscribed and closed since Range picked it up
		}

		if m.delivery == deliveryAsync {
			go s.send(ctx, m)
			n++
		} else if s.send(ctx, m) {
			n++
		}

		return true
	})

	return n, nil
}

// Close unsubscribes every subscriber, makes every later Subscribe, SubscribeSeq and Broadcast fail with ErrClosed, and
// waits until the goroutine of every subscriber from Subscribe it unsubscribed has processed what it was sent and
// ended. It does not wait for the subscribers unsubscribed before it was called. If ctx is done first, Close returns
// its error, and the subscribers go on draining in the background. Calling Close again returns ErrClosed right away.
//
// Close does not wait for the range loops over SubscribeSeq, which run in the caller's goroutines: each ends once it
// has yielded what its subscriber had already taken. A Broadcast that was already running when Close was called may
// still report errors to error handlers after Close has returned.
//
// Close cannot wait for the goroutine it is called from. So from handle, or from an error handler given a *HandleError
// or a *PanicError, pass it the ctx they were given, or one derived from it: Close then waits for every subscriber but
// that one. With any other ctx, Close waits for handle to return, which it cannot do, until ctx is done.
func (b *Broadcastor[T]) Close(ctx context.Context) error {
	if b.closed.Swap(true) {
		return ErrClosed
	}

	// Unsubscribed first, and only then waited for, so that none of them is still sent messages meanwhile.
	self, _ := ctx.Value(subscriberKey{}).(*subscriber[T])
	var draining []*subscriber[T]
	for key := range b.subscribers.Range {
		id, _ := key.(uuid.UUID)
		// done is nil for a subscriber from SubscribeSeq, whose loop Close does not wait for.
		if s := b.remove(id); s != nil && s.done != nil && s != self {
			draining = append(draining, s)
		}
	}

	for _, s := range draining {
		select {
		case <-s.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// subscribe adds a subscriber set up with options, and starts its goroutine calling handle, unless handle is nil.
func (b *Broadcastor[T]) subscribe(ctx context.Context, handle func(ctx context.Context, msg T) error, options []SubscriberOption[T]) (*subscriber[T], error) {
	if b.closed.Load() {
		return nil, ErrClosed
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	s := &subscriber[T]{id: id, ch: make(chan message[T])}
	for _, option := range options {
		option(s)
	}
	s.refs.Store(1)
	if handle != nil {
		s.done = make(chan struct{})
		go s.consume(ctx, handle)
	}
	b.subscribers.Store(id, s)

	// Close may have started since closed was checked above. It sets closed before it looks for subscribers, and this
	// checks it again after Store: so either Close finds this subscriber, or this sees closed and unsubscribes it, as
	// Close would have. Checking before Store only would leave it subscribed if Close looked in between.
	if b.closed.Load() {
		b.remove(id)
	}

	return s, nil
}

// remove unsubscribes the subscriber with the given ID and returns it, or returns nil if there was none.
func (b *Broadcastor[T]) remove(id uuid.UUID) *subscriber[T] {
	v, _ := b.subscribers.LoadAndDelete(id)
	s, ok := v.(*subscriber[T])
	if !ok {
		return nil
	}

	// Closes the channel now, or once the last Broadcast still sending on it is done.
	s.release()

	return s
}

// leave ends a subscription from SubscribeSeq once its loop is over: it unsubscribes s, unless that is already done,
// and then drops whatever s is still sent until its channel is closed, so that no Broadcast is left waiting on a loop
// that has ended. That wait is short: the only Broadcasts still sending on the channel are those that took a reference
// before the unsubscription, and each of them is done once the message it is sending here is taken.
func (b *Broadcastor[T]) leave(s *subscriber[T]) {
	b.remove(s.id)
	for range s.ch {
		// Dropped: the loop is over.
	}
}
