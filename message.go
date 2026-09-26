package broadcastor

import (
	"context"
	"time"
)

// message is how a single Broadcast sends its message to one subscriber, as set by the subscriber's default message
// options and then the Broadcast's own.
type message[T any] struct {
	value T

	async bool

	// timeout bounds how long Broadcast waits for the subscriber to take the message, on top of ctx, or is 0 for none.
	timeout time.Duration

	// errorHandler is called with every error about this message, on top of the subscriber's own, or nil for none.
	errorHandler func(ctx context.Context, err error)
}
