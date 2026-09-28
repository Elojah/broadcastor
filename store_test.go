package broadcastor_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
)

var errPut = errors.New("put failed")

// The store is given every message handle fails on, with the subscriber's ID and the error as handle returned it, right
// before the error handler is given that error. Messages handled without error are not stored.
func TestSubscriberWithStore(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		events := &recorder[string]{}
		stored := &recordStore{}
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			if msg%2 == 0 {
				return handleError(msg)
			}
			events.record(fmt.Sprintf("handled %d", msg))

			return nil
		},
			subscriber.WithStore[int](storeFunc(func(ctx context.Context, r subscriber.Record[int]) error {
				events.record(fmt.Sprintf("stored %d", r.Message))

				return stored.Put(ctx, r)
			})),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
				events.record(fmt.Sprintf("reported %v", err))
			}),
		)

		for msg := 1; msg <= 4; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		unsubscribe(t, b, id)
		synctest.Wait()

		want := []string{
			"handled 1", "stored 2", "reported " + handleError(2).Error(),
			"handled 3", "stored 4", "reported " + handleError(4).Error(),
		}
		if got := events.messages(); !slices.Equal(got, want) {
			t.Errorf("got events %q, want %q", got, want)
		}
		records := stored.messages()
		if len(records) != 2 {
			t.Fatalf("store got %v, want a record for each of messages 2 and 4", records)
		}
		for i, r := range records {
			msg := 2 * (i + 1)
			if r.SubscriberID != id || r.Message != msg || !errors.Is(r.Err, handleError(msg)) {
				t.Errorf("record %d is for subscriber %s and message %d with error %v, want %s, %d and %v",
					i, r.SubscriberID, r.Message, r.Err, id, msg, handleError(msg))
			}
		}
	})
}

// Whatever the reason a subscriber loses a message, the store is given it, with the error the error handlers would be
// given, even when the subscriber has no error handler.
func TestSubscriberWithStore_Lost(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		want error
		// lose makes a subscriber with the store option lose message 1, and returns its ID.
		lose func(t *testing.T, b *broadcastor.Broadcastor[int], store subscriber.Option[int]) uuid.UUID
	}{
		{"handle", handleError(1), func(t *testing.T, b *broadcastor.Broadcastor[int], store subscriber.Option[int]) uuid.UUID {
			t.Helper()
			id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
				return handleError(msg)
			}, store)
			b.Broadcast(t.Context(), 1)
			unsubscribe(t, b, id)

			return id
		}},
		{"Panic", subscriber.ErrPanic, func(t *testing.T, b *broadcastor.Broadcastor[int], store subscriber.Option[int]) uuid.UUID {
			t.Helper()
			id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
				panic(handleError(msg))
			}, store, subscriber.WithMiddleware(middleware.Recover[int]()))
			b.Broadcast(t.Context(), 1)
			unsubscribe(t, b, id)

			return id
		}},
		{"Timeout", subscriber.ErrTimeout, func(t *testing.T, b *broadcastor.Broadcastor[int], store subscriber.Option[int]) uuid.UUID {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, b, stuck.handle, store)
			b.Broadcast(t.Context(), 0) // stuck is now processing 0 and not reading
			b.Broadcast(t.Context(), 1, message.WithTimeout[int](time.Second))
			unsubscribe(t, b, id)
			stuck.release()

			return id
		}},
		{"Dropped", subscriber.ErrDropped, func(t *testing.T, b *broadcastor.Broadcastor[int], store subscriber.Option[int]) uuid.UUID {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, b, stuck.handle, store)
			b.Broadcast(t.Context(), 0) // stuck is now processing 0 and not reading
			b.Broadcast(t.Context(), 1, message.WithNonBlocking[int]())
			unsubscribe(t, b, id)
			stuck.release()

			return id
		}},
		{"UnsubscribeDiscard", subscriber.ErrClosed, func(t *testing.T, b *broadcastor.Broadcastor[int], store subscriber.Option[int]) uuid.UUID {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, b, stuck.handle, store, subscriber.WithBuffer[int](1))
			b.Broadcast(t.Context(), 0) // stuck holds on to 0, and 1 fills the buffer
			b.Broadcast(t.Context(), 1)
			if err := b.Unsubscribe(t.Context(), id, subscriber.WithUnsubscribeDiscard()); err != nil {
				t.Fatalf("Unsubscribe: %v", err)
			}
			stuck.release()

			return id
		}},
		{"SubscribeSeq", subscriber.ErrClosed, func(t *testing.T, b *broadcastor.Broadcastor[int], store subscriber.Option[int]) uuid.UUID {
			t.Helper()
			id, seq := subscribeSeq(t, b, subscriber.WithBuffer[int](2), store)
			b.Broadcast(t.Context(), 0)
			b.Broadcast(t.Context(), 1)
			for range seq {
				break // 1 is left in the buffer
			}

			return id
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := broadcastor.NewBroadcastor[int]()
				stored := &recordStore{}
				id := tt.lose(t, b, subscriber.WithStore[int](stored))
				synctest.Wait()

				records := stored.messages()
				if len(records) != 1 {
					t.Fatalf("store got %v, want a single record for message 1", records)
				}
				if r := records[0]; r.SubscriberID != id || r.Message != 1 || !errors.Is(r.Err, tt.want) {
					t.Errorf("store got a record for subscriber %s and message %d with error %v, want %s, 1 and %v",
						r.SubscriberID, r.Message, r.Err, id, tt.want)
				}
			})
		})
	}
}

// When Put fails, the error handlers are given a *StoreError instead, once, which matches both ErrStore and the error
// about the message, so an error handler looking for the latter still finds it. The message is not given to the store
// again.
func TestSubscriberWithStore_PutFails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stored := &recordStore{err: errPut}
		failures := &recorder[int]{}
		recordFailure := recordFailures(t, failures)
		errs := &recorder[error]{}
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			return handleError(msg)
		},
			subscriber.WithStore[int](stored),
			subscriber.WithErrorHandler[int](func(ctx context.Context, err error) {
				errs.record(err)
				recordFailure(ctx, err)
			}),
		)

		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		unsubscribe(t, b, id)
		synctest.Wait()

		if got, want := failures.messages(), []int{1, 2}; !slices.Equal(got, want) {
			t.Errorf("error handler found handleErrors for messages %v, want %v", got, want)
		}
		if got := stored.messages(); len(got) != 2 {
			t.Errorf("store got %v, want a single record for each of messages 1 and 2", got)
		}
		for i, err := range errs.messages() {
			msg := i + 1
			var storeErr *subscriber.StoreError[int]
			if !errors.As(err, &storeErr) || !errors.Is(err, subscriber.ErrStore) {
				t.Errorf("error %d = %v, want a *StoreError", i, err)

				continue
			}
			if storeErr.SubscriberID != id || storeErr.Message != msg || !errors.Is(storeErr.Err, errPut) ||
				!errors.Is(storeErr.Cause, handleError(msg)) {
				t.Errorf("error %d is for subscriber %s and message %d, failed with %v storing %v, want %s, %d, %v and %v",
					i, storeErr.SubscriberID, storeErr.Message, storeErr.Err, storeErr.Cause, id, msg, errPut, handleError(msg))
			}
		}
	})
}

// Put is given a ctx with the values of the ctx the message is handled with, the one message.WithContext gave it or else
// the subscriber's, but never done, even when that ctx is, as it is here: the subscriber's ctx being done unsubscribes it
// and makes it discard what is left in its buffer.
func TestSubscriberWithStore_Context(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		ctx, cancel := context.WithCancel(context.WithValue(subscribeCtx(t), key{}, "subscribe"))
		messageCtx, cancelMessage := context.WithCancel(context.WithValue(t.Context(), key{}, "message"))
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{hold: make(chan struct{})}
		puts := &recorder[string]{}
		_, err := b.Subscribe(ctx, handled.handle, subscriber.WithBuffer[int](2),
			subscriber.WithDefaultUnsubscribeOptions[int](subscriber.WithUnsubscribeDiscard()),
			subscriber.WithStore[int](storeFunc(func(ctx context.Context, r subscriber.Record[int]) error {
				puts.record(fmt.Sprintf("%d: %v, %v", r.Message, ctx.Value(key{}), ctx.Err()))

				return nil
			})))
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		// handle holds on to 0, and 1 and 2 fill the buffer.
		b.Broadcast(t.Context(), 0)
		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2, message.WithContext[int](messageCtx))
		cancelMessage()
		cancel()
		synctest.Wait()
		handled.release()
		synctest.Wait()

		if got, want := handled.messages(), []int{0}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		if got, want := puts.messages(), []string{"1: subscribe, <nil>", "2: message, <nil>"}; !slices.Equal(got, want) {
			t.Errorf("Put got messages with ctx values and errors %q, want %q", got, want)
		}
	})
}

// Every message a Broadcast picks a subscriber up for is either handled or stored, never both and never twice, whatever
// the delivery mode and however it is lost: handle failing on it, Broadcast timing out or finding the subscriber busy,
// or Close making the subscriber discard it. Both subscribers share the store, which tells them apart by their ID.
func TestSubscriberWithStore_ExactlyOnce(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stored := &recordStore{}
		options := []subscriber.Option[int]{
			subscriber.WithBuffer[int](1),
			subscriber.WithStore[int](stored),
			subscriber.WithDefaultUnsubscribeOptions[int](subscriber.WithUnsubscribeDiscard()),
		}
		handled := &recorder[int]{}
		handleID := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			time.Sleep(time.Millisecond)
			if msg%3 == 0 {
				return handleError(msg)
			}
			handled.record(msg)

			return nil
		}, options...)
		yielded := &recorder[int]{}
		seqID, seq := subscribeSeq(t, b, options...)
		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			for msg := range seq {
				time.Sleep(time.Millisecond)
				yielded.record(msg)
			}
		}()

		const broadcasters, perBroadcaster = 4, 50
		deliveries := []message.Option[int]{
			message.WithSync[int](), message.WithParallel[int](), message.WithAsync[int](), message.WithNonBlocking[int](),
		}
		var wg sync.WaitGroup
		for g := range broadcasters {
			wg.Go(func() {
				for i := range perBroadcaster {
					b.Broadcast(t.Context(), g*perBroadcaster+i, deliveries[i%len(deliveries)],
						message.WithTimeout[int](time.Millisecond))
				}
			})
		}
		waitGroup(t, &wg, "broadcasters")
		if err := b.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		waitClosed(t, loopDone, "SubscribeSeq loop ended by Close")
		synctest.Wait()

		all := make([]int, broadcasters*perBroadcaster)
		for i := range all {
			all[i] = i
		}
		records := stored.messages()
		for id, got := range map[uuid.UUID][]int{handleID: handled.messages(), seqID: yielded.messages()} {
			var lost []int
			for _, r := range records {
				if r.SubscriberID != id {
					continue
				}
				lost = append(lost, r.Message)
				if !errors.Is(r.Err, subscriber.ErrTimeout) && !errors.Is(r.Err, subscriber.ErrDropped) &&
					!errors.Is(r.Err, subscriber.ErrClosed) && !errors.Is(r.Err, handleError(r.Message)) {
					t.Errorf("subscriber %s: message %d stored with error %v, want a handleError, *TimeoutError, "+
						"*DroppedError or *ClosedError", id, r.Message, r.Err)
				}
			}
			if len(got) == 0 || len(lost) == 0 {
				t.Errorf("subscriber %s handled %d messages and lost %d, want some of each", id, len(got), len(lost))
			}
			if both := slices.Sorted(slices.Values(append(slices.Clone(got), lost...))); !slices.Equal(both, all) {
				t.Errorf("subscriber %s: handled %v and stored %v, want every message from 0 to %d once",
					id, got, lost, len(all)-1)
			}
		}
		for _, r := range records {
			if r.SubscriberID != handleID && r.SubscriberID != seqID {
				t.Errorf("store got a record for subscriber %s, want %s or %s", r.SubscriberID, handleID, seqID)
			}
		}
	})
}

// recordStore is a subscriber.Store that records every Record it is given, in order, and fails with err if set.
type recordStore struct {
	recorder[subscriber.Record[int]]

	err error
}

func (s *recordStore) Put(_ context.Context, r subscriber.Record[int]) error {
	s.record(r)

	return s.err
}

// storeFunc makes a func a subscriber.Store.
type storeFunc func(ctx context.Context, r subscriber.Record[int]) error

func (f storeFunc) Put(ctx context.Context, r subscriber.Record[int]) error {
	return f(ctx, r)
}
