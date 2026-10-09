// middleware.MaxAge does not hand handle a message older than d, since a reading that waited behind a slow handle may
// be worse than none. Each reading carries when it was taken. handle is busy with reading 1 while reading 2 waits in
// the buffer too long, so the error handlers get a *subscriber.ExpiredError for it instead. Reading 3 is fresh, and
// handled.
//
// handle waits for release, standing in for slow work.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
)

type reading struct {
	n  int
	at time.Time
}

const maxAge = 50 * time.Millisecond

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[reading]()

	release, done := make(chan struct{}), make(chan struct{})
	_, err := b.Subscribe(ctx, func(_ context.Context, _ uuid.UUID, r reading) error {
		fmt.Println("handled", r.n)
		switch r.n {
		case 1:
			<-release
		case 3:
			close(done)
		}

		return nil
	},
		subscriber.WithBuffer[reading](1),
		subscriber.WithMiddleware(middleware.MaxAge(maxAge, func(r reading) time.Time { return r.at })),
		subscriber.WithErrorHandler[reading](func(_ context.Context, err error) {
			var expired *subscriber.ExpiredError[reading]
			if errors.As(err, &expired) {
				fmt.Println("expired", expired.Message.n, "older than", maxAge)
			}
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	b.Broadcast(ctx, reading{n: 1, at: time.Now()}) // handle takes it, then waits
	b.Broadcast(ctx, reading{n: 2, at: time.Now()}) // waits in the buffer
	time.Sleep(2 * maxAge)
	close(release)
	b.Broadcast(ctx, reading{n: 3, at: time.Now()})
	<-done

	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
}
