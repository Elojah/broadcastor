// How long Broadcast waits for a busy subscriber depends on the delivery. The subscriber takes 1 and waits in handle,
// with room for one more message in its buffer (subscriber.WithBuffer):
//
//   - 2 goes in the buffer, so Broadcast does not wait.
//   - 3 is non-blocking (message.WithNonBlocking): the buffer is full, so it is dropped.
//   - 4 has a timeout (message.WithTimeout): Broadcast waits for room, then gives up.
//   - 5 is async (message.WithAsync): Broadcast returns at once, and a goroutine sends it once there is room.
//
// The error handler learns what was lost, from Broadcast's goroutine. handle waits for release, standing in for slow
// work.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/elojah/multicast"
	"github.com/elojah/multicast/message"
	"github.com/elojah/multicast/subscriber"
)

func main() {
	ctx := context.Background()
	b := multicast.New[int]()

	release := make(chan struct{})
	var handled sync.WaitGroup
	_, err := b.Subscribe(ctx, func(_ context.Context, _ uint64, n int) error {
		<-release
		fmt.Println("handled", n)
		handled.Done()

		return nil
	},
		subscriber.WithBuffer[int](1),
		subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
			var (
				dropped *subscriber.DroppedError[int]
				timeout *subscriber.TimeoutError[int]
			)
			switch {
			case errors.As(err, &dropped):
				fmt.Println("dropped", dropped.Message)
			case errors.As(err, &timeout):
				fmt.Println("timed out", timeout.Message)
			}
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	handled.Add(3)
	for _, m := range []struct {
		n       int
		options []message.Option[int]
	}{
		{1, nil},
		{2, nil},
		{3, []message.Option[int]{message.WithNonBlocking[int]()}},
		{4, []message.Option[int]{message.WithTimeout[int](10 * time.Millisecond)}},
		{5, []message.Option[int]{message.WithAsync[int]()}},
	} {
		fmt.Println(m.n, "handed to", b.Broadcast(ctx, m.n, m.options...), "subscribers")
	}
	close(release)
	handled.Wait()

	if err := b.Close(); err != nil {
		log.Fatal(err)
	}
}
