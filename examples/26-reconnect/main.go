// A forwarder sends each message over a link, which goes down on 3. It reconnects in place: handle closes the link once
// a send fails and dials a new one on the next attempt, which middleware.Retry makes. Meanwhile, 4 and 5 wait in the
// buffer, so the new link sends 3, 4 then 5, in order. subscriber.WithDone tells main when to close the last link.
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

var errDown = errors.New("down")

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

// subscribeForwarder subscribes the forwarder, which sends each message over its own link, and dials a new one after a
// failed send. It returns a func that closes the last link once the forwarder is done.
func subscribeForwarder(ctx context.Context, b *broadcastor.Broadcastor[int], n *network) func() {
	// Only the forwarder's goroutine uses it in handle, and the returned func once done has the ID, so it needs no
	// lock. nil once closed, until the next attempt.
	l := n.dial()
	handle := func(_ context.Context, _ uuid.UUID, msg int) error {
		if l == nil {
			l = n.dial()
		}
		if err := l.send(msg); err != nil {
			fmt.Println(err)
			l.close()
			l = nil

			return err
		}

		return nil
	}

	// Until the link is back up: the subscription's ctx bounds it.
	retry := middleware.Retry[int](middleware.RetryPolicy{
		Attempts: math.MaxInt, Delay: time.Millisecond, Multiplier: 2, MaxDelay: time.Second,
		IsRetryable: func(err error) bool { return errors.Is(err, errDown) },
	})
	// Room for the forwarder's ID, since Shutdown waits for the send.
	done := make(chan uuid.UUID, 1)
	_, err := b.Subscribe(ctx, handle,
		subscriber.WithMiddleware(retry),
		// What Broadcast hands the forwarder while it reconnects waits there, in order.
		subscriber.WithBuffer[int](8),
		subscriber.WithDone[int](ctx, done),
	)
	if err != nil {
		log.Fatal(err)
	}

	return func() {
		<-done // sent after handle's last call
		if l != nil {
			l.close()
		}
	}
}

// network dials links. The first goes down on 3, and dialling the second waits until up is closed.
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
		l.downOn = 3
	case 2:
		close(n.dialling)
		<-n.up
	}

	return l
}

// link stands in for a connection to an uplink.
type link struct {
	id int
	// downOn is the message the link goes down on, 0 for none.
	downOn int
}

// send sends msg over the link.
func (l *link) send(msg int) error {
	if msg == l.downOn {
		return fmt.Errorf("link %d: %w", l.id, errDown)
	}
	fmt.Printf("link %d: %d\n", l.id, msg)

	return nil
}

// close closes the link.
func (l *link) close() {
	fmt.Printf("link %d: closed\n", l.id)
}
