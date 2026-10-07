package store_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

var (
	errHandle = errors.New("handle failed")
	errPut    = errors.New("put failed")
)

// Drain hands every entry to handle, oldest first, with the ID of the subscriber that lost it, acks it, and waits for
// more until ctx is done.
func TestDrain(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		r := store.NewRing[int](4)
		id := uuid.New()
		for msg := 1; msg <= 3; msg++ {
			put(t, r, subscriber.Record[int]{SubscriberID: id, Message: msg, Err: errLost})
		}
		var handled []int
		ctx, cancel := context.WithCancel(t.Context())
		done := goDrain(ctx, r, func(_ context.Context, gotID uuid.UUID, msg int) error {
			if gotID != id {
				t.Errorf("handle got subscriber %s, want %s", gotID, id)
			}
			handled = append(handled, msg)

			return nil
		}, nil)
		synctest.Wait()
		put(t, r, subscriber.Record[int]{SubscriberID: id, Message: 4})
		synctest.Wait()
		cancel()

		if err := receiveErr(t, done); !errors.Is(err, context.Canceled) {
			t.Errorf("Drain = %v, want %v", err, context.Canceled)
		}
		if want := []int{1, 2, 3, 4}; !slices.Equal(handled, want) {
			t.Errorf("handle got %v, want %v", handled, want)
		}
		if n := r.Len(); n != 0 {
			t.Errorf("Len = %d once drained, want 0", n)
		}
	})
}

// A failed entry goes to the dead letters, with handle's error and a ctx never done, or is dropped without them. Either
// way it is acked, and Drain goes on.
func TestDrain_DeadLetter(t *testing.T) {
	t.Parallel()

	for _, withDeadLetter := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			type key struct{}
			r := store.NewRing[int](4)
			id := uuid.New()
			for msg := 1; msg <= 4; msg++ {
				put(t, r, subscriber.Record[int]{SubscriberID: id, Message: msg})
			}
			var dead []subscriber.Record[int]
			var deadLetters subscriber.Store[int]
			if withDeadLetter {
				deadLetters = store.PutFunc[int](func(ctx context.Context, r subscriber.Record[int]) error {
					if v := ctx.Value(key{}); v != "drain" || ctx.Done() != nil {
						t.Errorf("dead letter Put got a ctx with value %v that can be done, want Drain's, never done", v)
					}
					dead = append(dead, r)

					return nil
				})
			}
			ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, "drain"))
			done := goDrain(ctx, r, func(_ context.Context, _ uuid.UUID, msg int) error {
				if msg%2 == 0 {
					return errHandle
				}

				return nil
			}, deadLetters)
			synctest.Wait()
			cancel()

			if err := receiveErr(t, done); !errors.Is(err, context.Canceled) {
				t.Errorf("Drain = %v, want %v", err, context.Canceled)
			}
			if n := r.Len(); n != 0 {
				t.Errorf("Len = %d once drained, want 0", n)
			}
			var want []subscriber.Record[int]
			if withDeadLetter {
				want = []subscriber.Record[int]{{SubscriberID: id, Message: 2, Err: errHandle}, {SubscriberID: id, Message: 4, Err: errHandle}}
			}
			if !slices.Equal(dead, want) {
				t.Errorf("dead letter got %v, want %v", dead, want)
			}
		})
	}
}

// An entry handle fails on once ctx is done stays in the queue, for the next Drain.
func TestDrain_ContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		r := store.NewRing[int](2)
		putMessages(t, r, 1, 2)
		ctx, cancel := context.WithCancel(t.Context())
		done := goDrain(ctx, r, func(ctx context.Context, _ uuid.UUID, _ int) error {
			<-ctx.Done()

			return ctx.Err()
		}, store.PutFunc[int](func(_ context.Context, r subscriber.Record[int]) error {
			t.Errorf("dead letter got %+v, want nothing", r)

			return nil
		}))
		synctest.Wait()
		cancel()

		if err := receiveErr(t, done); !errors.Is(err, context.Canceled) {
			t.Errorf("Drain = %v, want %v", err, context.Canceled)
		}
		if got, want := readAll(t, r), []int{1, 2}; !slices.Equal(got, want) {
			t.Errorf("left %v in the queue, want %v", got, want)
		}
	})
}

// An entry handled just as ctx is done is acked all the same, so that it is not handled again.
func TestDrain_AckOnceHandled(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		q := &strictQueue{Ring: store.NewRing[int](2)}
		putMessages(t, q.Ring, 1, 2)
		ctx, cancel := context.WithCancel(t.Context())
		done := goDrain(ctx, q, func(context.Context, uuid.UUID, int) error {
			cancel()

			return nil
		}, nil)

		if err := receiveErr(t, done); !errors.Is(err, context.Canceled) {
			t.Errorf("Drain = %v, want %v", err, context.Canceled)
		}
		if got, want := readAll(t, q.Ring), []int{2}; !slices.Equal(got, want) {
			t.Errorf("left %v in the queue, want %v", got, want)
		}
	})
}

// When the dead letter store fails, Drain returns its error and leaves the entry in the queue.
func TestDrain_DeadLetterFails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		r := store.NewRing[int](2)
		putMessages(t, r, 1)
		done := goDrain(t.Context(), r, func(context.Context, uuid.UUID, int) error {
			return errHandle
		}, store.PutFunc[int](func(context.Context, subscriber.Record[int]) error {
			return errPut
		}))

		if err := receiveErr(t, done); !errors.Is(err, errPut) {
			t.Errorf("Drain = %v, want %v", err, errPut)
		}
		if n := r.Len(); n != 1 {
			t.Errorf("Len = %d, want 1", n)
		}
	})
}

// When Ack fails, Drain returns its error, and the entry stays in the queue.
func TestDrain_AckFails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		q := &strictQueue{Ring: store.NewRing[int](2), err: errPut}
		putMessages(t, q.Ring, 1)
		done := goDrain(t.Context(), q, func(context.Context, uuid.UUID, int) error { return nil }, nil)

		if err := receiveErr(t, done); !errors.Is(err, errPut) {
			t.Errorf("Drain = %v, want %v", err, errPut)
		}
		if n := q.Len(); n != 1 {
			t.Errorf("Len = %d, want 1", n)
		}
	})
}

// With middleware.Retry, Drain hands handle the next entry only once the previous one succeeds, so order is kept.
func TestDrain_Retry(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		r := store.NewRing[int](2)
		putMessages(t, r, 1, 2)
		var calls []int
		failures := 2
		handle := middleware.Retry[int](middleware.RetryPolicy{Attempts: 5, Delay: time.Second})(
			func(_ context.Context, _ uuid.UUID, msg int) error {
				calls = append(calls, msg)
				if failures > 0 {
					failures--

					return errHandle
				}

				return nil
			})
		ctx, cancel := context.WithCancel(t.Context())
		done := goDrain(ctx, r, handle, nil)
		time.Sleep(3 * time.Second)
		cancel()

		if err := receiveErr(t, done); !errors.Is(err, context.Canceled) {
			t.Errorf("Drain = %v, want %v", err, context.Canceled)
		}
		if want := []int{1, 1, 1, 2}; !slices.Equal(calls, want) {
			t.Errorf("handle calls were for %v, want %v", calls, want)
		}
	})
}

// Enqueue's handle puts the message with the subscriber's ID, no error, and a ctx with the same values, never done, and
// returns Put's error.
func TestEnqueue(t *testing.T) {
	t.Parallel()

	type key struct{}
	id := uuid.New()
	var mu sync.Mutex
	var got []subscriber.Record[int]
	handle := store.Enqueue(store.PutFunc[int](func(ctx context.Context, r subscriber.Record[int]) error {
		if v := ctx.Value(key{}); v != "handle" || ctx.Err() != nil {
			t.Errorf("Put got a ctx with value %v and error %v, want handle's, not done", v, ctx.Err())
		}
		mu.Lock()
		defer mu.Unlock()
		got = append(got, r)
		if r.Message == 2 {
			return errPut
		}

		return nil
	}))

	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, "handle"))
	cancel()
	if err := handle(ctx, id, 1); err != nil {
		t.Errorf("handle(1) = %v, want nil", err)
	}
	if err := handle(ctx, id, 2); !errors.Is(err, errPut) {
		t.Errorf("handle(2) = %v, want %v", err, errPut)
	}
	want := []subscriber.Record[int]{{SubscriberID: id, Message: 1}, {SubscriberID: id, Message: 2}}
	if !slices.Equal(got, want) {
		t.Errorf("Put got %v, want %v", got, want)
	}
}

// strictQueue is a Ring whose Ack fails with err, or once ctx is done, as a queue with a backend would.
type strictQueue struct {
	*store.Ring[int]

	err error
}

func (q *strictQueue) Ack(ctx context.Context, id string) error {
	if q.err != nil {
		return q.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	return q.Ring.Ack(ctx, id)
}

func receiveErr(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(deadlockTimeout):
		t.Fatalf("still waiting for Drain after %v: deadlock", deadlockTimeout)

		return nil
	}
}

// goDrain calls store.Drain from a goroutine of its own, and returns a channel that yields what it returns.
func goDrain(
	ctx context.Context, q store.Queue[int], handle subscriber.Handler[int], deadLetters subscriber.Store[int],
) <-chan error {
	done := make(chan error, 1)
	go func() { done <- store.Drain(ctx, q, handle, deadLetters) }()

	return done
}
