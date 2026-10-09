// A forwarder sends each message over a link, which goes down on 3. Two middlewares of its own reconnect it in place:
// disconnect finds the link down after 3 failed sends in a row, then reconnect dials a new one, with
// middleware.RetryPolicy's backoff, and sends 3 again. middleware.Retry retries the sends until then. Meanwhile, 4 and
// 5 wait in the buffer, so the new link sends 3, 4 then 5, in order. subscriber.WithDone tells main when to close the
// last link.
//
// Dialling waits until main brings the network back up, standing in for a slow reconnection.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
)

var (
	errDown = errors.New("down")
	// errDisconnected is what disconnect wraps around the error that finds the link down, for reconnect.
	errDisconnected = errors.New("disconnected")
)

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[int]()
	n := &network{dialling: make(chan struct{}), up: make(chan struct{})}
	closeLink := subscribeForwarder(ctx, b, n)

	for msg := 1; msg <= 3; msg++ {
		b.Broadcast(ctx, msg)
	}
	<-n.dialling
	for msg := 4; msg <= 5; msg++ {
		fmt.Println(msg, "handed to", b.Broadcast(ctx, msg), "subscribers")
	}
	close(n.up)

	if err := b.Shutdown(ctx); err != nil {
		log.Fatal(err)
	}
	closeLink()
}

// subscribeForwarder subscribes the forwarder, which sends each message over its link. It returns a func that closes the
// last link once the forwarder is done.
func subscribeForwarder(ctx context.Context, b *broadcastor.Broadcastor[int], n *network) func() {
	// Only the forwarder's goroutine uses it, in handle and redial, and the returned func once done has the ID, so it
	// needs no lock.
	l := n.dial()
	handle := func(_ context.Context, _ uuid.UUID, msg int) error {
		return l.send(msg)
	}
	redial := func(context.Context) error {
		l.close()
		l = n.dial()

		return nil
	}

	// Room for the forwarder's ID, since Shutdown waits for the send.
	done := make(chan uuid.UUID, 1)
	_, err := b.Subscribe(ctx, handle,
		subscriber.WithMiddleware(
			middleware.Retry[int](middleware.RetryPolicy{Attempts: 5, Delay: time.Millisecond}),
			// Until the link is back up: the subscription's ctx bounds it.
			reconnect[int](redial, middleware.RetryPolicy{
				Attempts: math.MaxInt, Delay: time.Millisecond, Multiplier: 2, MaxDelay: time.Second,
			}),
			disconnect[int](3),
		),
		// What Broadcast hands the forwarder while it reconnects waits there, in order.
		subscriber.WithBuffer[int](8),
		subscriber.WithDone[int](ctx, done),
	)
	if err != nil {
		log.Fatal(err)
	}

	return func() {
		<-done // sent after handle's last call
		l.close()
	}
}

// disconnect wraps errDisconnected around every n-th error in a row.
func disconnect[T any](n int) subscriber.Middleware[T] {
	return func(next subscriber.Handler[T]) subscriber.Handler[T] {
		// One count per subscriber, which handles one message at a time.
		failures := 0

		return func(ctx context.Context, id uuid.UUID, msg T) error {
			err := next(ctx, id, msg)
			if err == nil {
				failures = 0

				return nil
			}
			if failures++; failures < n {
				return err
			}
			failures = 0

			return fmt.Errorf("%w: %w", errDisconnected, err)
		}
	}
}

// reconnect calls redial once next returns errDisconnected, as policy retries it, then hands next the message again.
func reconnect[T any](redial func(ctx context.Context) error, policy middleware.RetryPolicy) subscriber.Middleware[T] {
	return func(next subscriber.Handler[T]) subscriber.Handler[T] {
		return func(ctx context.Context, id uuid.UUID, msg T) error {
			if err := next(ctx, id, msg); !errors.Is(err, errDisconnected) {
				return err
			}
			if err := policy.Do(ctx, redial); err != nil {
				return err
			}

			return next(ctx, id, msg)
		}
	}
}

// network dials links. The first goes down on 3, for good, and dialling the second waits until up is closed.
type network struct {
	links    int
	dialling chan struct{}
	up       chan struct{}
}

// dial returns a new link.
func (n *network) dial() *link {
	n.links++
	l := &link{id: n.links}
	switch l.id {
	case 1:
		l.downFrom = 3
	case 2:
		close(n.dialling)
		<-n.up
	}

	return l
}

// link stands in for a connection to an uplink.
type link struct {
	id int
	// downFrom is the first message the link fails to send, 0 for none.
	downFrom int
}

// send sends msg over the link.
func (l *link) send(msg int) error {
	if l.downFrom != 0 && msg >= l.downFrom {
		err := fmt.Errorf("link %d: %w", l.id, errDown)
		fmt.Println(err)

		return err
	}
	fmt.Printf("link %d: %d\n", l.id, msg)

	return nil
}

// close closes the link.
func (l *link) close() {
	fmt.Printf("link %d: closed\n", l.id)
}
