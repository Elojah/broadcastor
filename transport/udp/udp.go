// Package udp carries messages over UDP: Receive broadcasts every datagram a net.PacketConn reads, and Send is a handle
// that writes every message as a datagram.
package udp

import (
	"context"
	"net"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
	"github.com/elojah/broadcastor/transport"
)

// maxDatagram is the largest UDP payload, so no datagram is truncated.
const maxDatagram = 1<<16 - 1

// Receive reads every datagram from conn, decodes it with the address it came from, and broadcasts it to p with ctx,
// until ctx is done or a read fails. b is only valid until decode returns. What decode fails on goes to the error
// handler (transport.WithErrorHandler).
//
// By default it broadcasts with message.WithNonBlocking: a subscriber that is busy misses the datagram, with a
// *subscriber.DroppedError, as it would miss it if the socket's buffer overflowed while Receive waited.
//
// Once ctx is done, it sets conn's read deadline to now, which stops the read, and returns ctx.Err(). Otherwise it
// returns the read's error, such as net.ErrClosed once conn is closed.
func Receive[T any](
	ctx context.Context, conn net.PacketConn, p transport.Publisher[T], decode func(b []byte, from net.Addr) (T, error),
	options ...transport.SourceOption[T],
) error {
	config := transport.NewSourceConfig([]message.Option[T]{message.WithNonBlocking[T]()}, options...)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()

	b := make([]byte, maxDatagram)
	for {
		n, from, err := conn.ReadFrom(b)
		// A datagram may be empty, and one may come with an error.
		if err == nil || n > 0 {
			if msg, err := decode(b[:n], from); err != nil {
				config.Report(ctx, err)
			} else {
				p.Broadcast(ctx, msg, config.MessageOptions...)
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			return err
		}
	}
}

// Send returns a handle that encodes every message into a datagram and the address to send it to, and writes it from
// conn. encode's error and the write's are handle's.
func Send[T any](conn net.PacketConn, encode func(msg T) ([]byte, net.Addr, error)) subscriber.Handler[T] {
	return func(_ context.Context, _ uuid.UUID, msg T) error {
		b, to, err := encode(msg)
		if err != nil {
			return err
		}
		_, err = conn.WriteTo(b, to)

		return err
	}
}
