package transport

import (
	"context"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

// Publisher is what a source broadcasts to, such as a *broadcastor.Broadcastor.
type Publisher[T any] interface {
	Broadcast(ctx context.Context, msg T, options ...message.Option[T]) int
}

// Subscribable is what a sink towards many clients subscribes to once per client, such as a *broadcastor.Broadcastor.
type Subscribable[T any] interface {
	Subscribe(ctx context.Context, handle func(ctx context.Context, id uuid.UUID, msg T) error, options ...subscriber.Option[T]) (uuid.UUID, error)
}

// SourceOption configures a source.
type SourceOption[T any] func(config *SourceConfig[T])

// SourceConfig is what SourceOptions set, for the packages implementing a source.
type SourceConfig[T any] struct {
	// MessageOptions are passed to every Broadcast.
	MessageOptions []message.Option[T]

	// ErrorHandler gets every error about a message the source could not broadcast, such as a decode error. nil means
	// none.
	ErrorHandler func(ctx context.Context, err error)
}

// NewSourceConfig applies options on top of a source's default message options.
func NewSourceConfig[T any](defaults []message.Option[T], options ...SourceOption[T]) SourceConfig[T] {
	config := SourceConfig[T]{MessageOptions: defaults}
	for _, option := range options {
		option(&config)
	}

	return config
}

// Report passes err to the error handler, if any.
func (c SourceConfig[T]) Report(ctx context.Context, err error) {
	if c.ErrorHandler != nil {
		c.ErrorHandler(ctx, err)
	}
}

// WithMessageOptions replaces the message options the source passes to every Broadcast. Like any Broadcast option, they
// override the subscribers' defaults. With none, the subscribers' defaults apply.
func WithMessageOptions[T any](options ...message.Option[T]) SourceOption[T] {
	return func(config *SourceConfig[T]) {
		config.MessageOptions = options
	}
}

// WithErrorHandler sets the handler for every error about a message the source could not broadcast. It runs in the
// source's goroutine, so a slow one holds the source up. Without one, those errors are discarded.
func WithErrorHandler[T any](handler func(ctx context.Context, err error)) SourceOption[T] {
	return func(config *SourceConfig[T]) {
		config.ErrorHandler = handler
	}
}
