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
