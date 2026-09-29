// Package message holds the options passed to Broadcast and the Message they configure.
package message

import (
	"context"
	"time"
)

// Delivery is how Broadcast hands a message to a subscriber. The last option that sets it wins.
type Delivery int

const (
	// DeliverySync waits for each subscriber in turn. It is the default.
	DeliverySync Delivery = iota
	// DeliveryParallel sends to every subscriber at once, then waits for all of them.
	DeliveryParallel
	// DeliveryAsync sends to every subscriber at once and returns right away.
	DeliveryAsync
	// DeliveryNonBlocking drops the message for a subscriber that cannot take it right away.
	DeliveryNonBlocking
)

// Message is what one Broadcast sends to one subscriber.
type Message[T any] struct {
	Value  T
	Config Config
}

// Config is how one Broadcast sends its message to one subscriber.
type Config struct {
	Delivery Delivery

	// Timeout bounds the wait for the subscriber to take the message. 0 means none.
	Timeout time.Duration

	// ErrorHandler gets every error about this message, after the subscriber's own. nil means none.
	ErrorHandler func(ctx context.Context, err error)

	// Context replaces the subscriber's ctx for this message. nil means none.
	Context context.Context //nolint:containedctx // it travels with the message, which outlives Broadcast
}

// New applies options on top of config, a subscriber's defaults, which are left untouched since config is a copy.
func New[T any](value T, config Config, options ...Option[T]) Message[T] {
	for _, option := range options {
		option(&config)
	}

	return Message[T]{Value: value, Config: config}
}
