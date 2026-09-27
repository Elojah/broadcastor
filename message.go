package broadcastor

import (
	"context"
	"time"
)

// delivery is how Broadcast hands a message to a subscriber. The modes exclude each other, so the last message option
// that sets one wins.
type delivery int

const (
	// deliverySync makes Broadcast wait for the subscriber to take the message. It is the default.
	deliverySync delivery = iota
	// deliveryAsync makes Broadcast wait for the subscriber from a goroutine of its own, and return right away.
	deliveryAsync
	// deliveryNonBlocking makes Broadcast drop the message if the subscriber cannot take it right away.
	deliveryNonBlocking
)

// message is how a single Broadcast sends its message to one subscriber, as set by the subscriber's default message
// options and then the Broadcast's own.
type message[T any] struct {
	value T

	delivery delivery

	// timeout bounds how long Broadcast waits for the subscriber to take the message, on top of ctx, or is 0 for none.
	timeout time.Duration

	// errorHandler is called with every error about this message, on top of the subscriber's own, or nil for none.
	errorHandler func(ctx context.Context, err error)

	// values is the Broadcast ctx, whose values a subscriber with WithSubscriberBroadcastValues processes m with, or nil.
	values context.Context //nolint:containedctx // carries the Broadcast ctx's values to handle, the whole point
}

// with returns what a Broadcast sends to one subscriber, starting from m, that subscriber's defaults: value, with the
// Broadcast's options applied on top, so that they override the defaults. m itself is left as it was.
func (m message[T]) with(value T, options ...MessageOptions[T]) message[T] {
	m.value = value
	for _, option := range options {
		option(&m)
	}

	return m
}

// handleContext returns the ctx that the subscriber, which was given ctx by Subscribe or SubscribeSeq, processes m with:
// ctx itself, or with WithSubscriberBroadcastValues a messageContext that adds the values of m's Broadcast ctx.
func (m message[T]) handleContext(ctx context.Context) context.Context {
	if m.values == nil {
		return ctx
	}

	return messageContext{Context: ctx, values: context.WithoutCancel(m.values)}
}

// messageContext is the ctx a subscriber with WithSubscriberBroadcastValues processes a message with. Its deadline and
// cancellation are those of the Subscribe ctx alone, since the Broadcast ctx may be done long before the subscriber
// takes the message, and its values are those of the Broadcast ctx, then those of the Subscribe ctx.
type messageContext struct {
	context.Context //nolint:containedctx // the Subscribe ctx, which it extends

	// values is the Broadcast ctx wrapped by context.WithoutCancel, which answers nil for the context package's own key
	// under which a cancellable ctx returns itself. Value then looks that key up on the Subscribe ctx, so that
	// context.Cause and the ctxs derived from this one follow the Subscribe ctx, never the Broadcast ctx.
	values context.Context //nolint:containedctx // see message.values
}

func (c messageContext) Value(key any) any {
	if v := c.values.Value(key); v != nil {
		return v
	}

	return c.Context.Value(key)
}
