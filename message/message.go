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

	// set holds the fields options set, which New lays over a subscriber's defaults even when zero.
	set fields
}

// fields is a set of Config fields.
type fields uint8

const (
	fieldDelivery fields = 1 << iota
	fieldTimeout
	fieldErrorHandler
	fieldContext
)

// NewConfig applies options once, for New to lay over each subscriber's defaults.
func NewConfig[T any](options ...Option[T]) Config {
	// Before declaring config, which passing it to the options moves to the heap.
	if len(options) == 0 {
		return Config{}
	}
	var config Config
	for _, option := range options {
		option(&config)
	}

	return config
}

// New returns value with config, from NewConfig, laid over defaults, a subscriber's: each field config's options set
// replaces the default. Both are copies, so nothing escapes.
func New[T any](value T, defaults, config Config) Message[T] {
	if config.set&fieldDelivery != 0 {
		defaults.Delivery = config.Delivery
	}
	if config.set&fieldTimeout != 0 {
		defaults.Timeout = config.Timeout
	}
	if config.set&fieldErrorHandler != 0 {
		defaults.ErrorHandler = config.ErrorHandler
	}
	if config.set&fieldContext != 0 {
		defaults.Context = config.Context
	}

	return Message[T]{Value: value, Config: defaults}
}
