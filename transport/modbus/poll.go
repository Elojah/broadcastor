package modbus

import (
	"context"
	"errors"
	"time"

	"github.com/elojah/sunspec/modbus"

	"github.com/elojah/broadcastor/message"
)

// Broadcaster is what Poll needs of a *broadcastor.Broadcastor[T].
type Broadcaster[T any] interface {
	Broadcast(ctx context.Context, msg T, options ...message.Option[T]) int
}

// Poll calls read at once then every interval, and broadcasts what it returns with options, until ctx is done or read
// fails with modbus.ErrClosed, and returns that error. A read failing otherwise goes to onError, unless nil, and is
// skipped: the next poll brings a newer value.
//
// A Broadcast waiting on a subscriber delays the next read, and the polls missed meanwhile are skipped, like
// time.Ticker's ticks. message.WithNonBlocking keeps the polls on time.
func Poll[T any](
	ctx context.Context, b Broadcaster[T], interval time.Duration, read func(ctx context.Context) (T, error),
	onError func(ctx context.Context, err error), options ...message.Option[T],
) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		msg, err := read(ctx)
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, modbus.ErrClosed):
			return err
		case err != nil:
			if onError != nil {
				onError(ctx, err)
			}
		default:
			b.Broadcast(ctx, msg, options...)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
