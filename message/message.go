// Package message holds how a broadcastor.Broadcastor hands a message to each subscriber: the options passed to
// Broadcast or to subscriber.WithDefaultMessageOptions, and the Message they set up. Broadcast is the Broadcastor's
// method.
package message

import (
	"context"
	"time"
)

// Delivery is how Broadcast hands a message to a subscriber. The modes exclude each other, so the last option that sets
// one wins.
type Delivery int

const (
	// DeliverySync makes Broadcast wait for the subscriber to take the message. It is the default.
	DeliverySync Delivery = iota
	// DeliveryParallel makes Broadcast wait for the subscriber from a goroutine of its own, and return once every such
	// goroutine is done.
	DeliveryParallel
	// DeliveryAsync makes Broadcast wait for the subscriber from a goroutine of its own, and return right away.
	DeliveryAsync
	// DeliveryNonBlocking makes Broadcast drop the message if the subscriber cannot take it right away.
	DeliveryNonBlocking
)

// Message is what a single Broadcast sends to one subscriber: the value, and how it is sent.
type Message[T any] struct {
	Value  T
	Config Config
}

// Config is how a single Broadcast sends its message to one subscriber, as set by the subscriber's default message
// options and then the Broadcast's own. Options change nothing else.
type Config struct {
	Delivery Delivery

	// Timeout bounds how long Broadcast waits for the subscriber to take the message, on top of ctx, or is 0 for none.
	Timeout time.Duration

	// ErrorHandler is called with every error about this message, on top of the subscriber's own, or nil for none.
	ErrorHandler func(ctx context.Context, err error)
}

// New returns what a Broadcast sends to one subscriber: value, with config, that subscriber's defaults, and the
// Broadcast's options applied on top, so that they override the defaults. config is a copy, so the defaults are left as
// they were.
func New[T any](value T, config Config, options ...Option[T]) Message[T] {
	for _, option := range options {
		option(&config)
	}

	return Message[T]{Value: value, Config: config}
}
