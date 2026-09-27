package broadcastor_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
)

// deadlockTimeout bounds every wait in these tests, so a deadlock fails the test instead of hanging until go test's own
// timeout. Inside a synctest bubble the fake clock only advances once every goroutine is blocked, so there it fires
// exactly when the bubble is deadlocked.
//
// Most tests run in a synctest bubble for another reason too: a subscriber's goroutine only ends once its channel is
// closed, and synctest.Test fails if any goroutine is left blocked when the test returns. So a channel that is never
// closed fails the test, with the stuck subscriber's goroutine in the trace, and synctest.Wait after an Unsubscribe
// waits for the subscriber to finish processing what it was sent.
const deadlockTimeout = 10 * time.Second

func TestBroadcast_DeliversAllMessagesInOrder(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		const subscribers = 3
		b := broadcastor.NewBroadcastor[int]()
		ids := make([]uuid.UUID, 0, subscribers)
		recorders := make([]*recorder[int], 0, subscribers)
		for range subscribers {
			r, id := record(t, b)
			ids = append(ids, id)
			recorders = append(recorders, r)
		}

		for msg := 1; msg <= 4; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		unsubscribeAll(t, b, ids...)
		synctest.Wait()

		for i, r := range recorders {
			if got, want := r.messages(), []int{1, 2, 3, 4}; !slices.Equal(got, want) {
				t.Errorf("subscriber %d received %v, want %v", i, got, want)
			}
		}
	})
}

func TestBroadcast_NoSubscribers(t *testing.T) {
	t.Parallel()

	b := broadcastor.NewBroadcastor[int]()
	b.Broadcast(t.Context(), 1)
}

// The error handler is given every error handle returns, in order, with the subscriber and the message it failed on,
// and both handle and the error handler are given the ctx that was passed to Subscribe.
func TestWithErrorHandler(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		ctx := context.WithValue(t.Context(), key{}, "subscribe")
		checkCtx := func(ctx context.Context, what string) {
			if v := ctx.Value(key{}); v != "subscribe" {
				t.Errorf("%s got a ctx with value %v, want the one passed to Subscribe", what, v)
			}
		}
		b := broadcastor.NewBroadcastor[int]()
		errs := &recorder[*broadcastor.HandleError[int]]{}
		id, err := b.Subscribe(ctx, func(ctx context.Context, msg int) error {
			checkCtx(ctx, "handle")
			if msg%2 == 1 {
				return handleError(msg)
			}

			return nil
		}, broadcastor.WithSubscriberErrorHandler[int](func(ctx context.Context, err error) {
			checkCtx(ctx, "the error handler")
			var handleErr *broadcastor.HandleError[int]
			if !errors.As(err, &handleErr) {
				t.Errorf("error handler got %v, want a *HandleError", err)

				return
			}
			errs.record(handleErr)
		}))
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		for msg := 1; msg <= 4; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		unsubscribe(t, b, id)
		synctest.Wait()

		got, failed := errs.messages(), []int{1, 3}
		if len(got) != len(failed) {
			t.Fatalf("error handler got %v, want one error for each of messages %v", got, failed)
		}
		for i, msg := range failed {
			err := got[i]
			if err.SubscriberID != id || err.Message != msg {
				t.Errorf("error %d is for subscriber %s and message %d, want %s and %d",
					i, err.SubscriberID, err.Message, id, msg)
			}
			if !errors.Is(err, handleError(msg)) {
				t.Errorf("error %d = %v, want it to wrap %v", i, err, handleError(msg))
			}
			if s := err.Error(); !strings.Contains(s, id.String()) || !strings.Contains(s, handleError(msg).Error()) {
				t.Errorf("error %q does not mention both the subscriber %s and the error from handle", s, id)
			}
		}
	})
}

// Without an error handler, errors from handle are discarded, and they do not hold the subscriber up.
func TestSubscribe_ErrorsWithoutHandler(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		r := &recorder[int]{}
		id := subscribe(t, b, func(_ context.Context, msg int) error {
			r.record(msg)

			return handleError(msg)
		})

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		for msg := range 3 {
			b.Broadcast(ctx, msg)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Broadcasts returned after %v, want no wait", elapsed)
		}

		unsubscribe(t, b, id)
		synctest.Wait()
		if got, want := r.messages(), []int{0, 1, 2}; !slices.Equal(got, want) {
			t.Errorf("subscriber processed %v, want %v", got, want)
		}
	})
}

func TestUnsubscribe_UnknownID(t *testing.T) {
	t.Parallel()

	b := broadcastor.NewBroadcastor[int]()
	for _, id := range []uuid.UUID{uuid.Nil, uuid.New()} {
		err := b.Unsubscribe(t.Context(), id)
		var notFound *broadcastor.SubscriberNotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("Unsubscribe(%s) = %v, want *SubscriberNotFoundError", id, err)
		}
		if notFound.SubscriberID != id {
			t.Errorf("error is for subscriber %s, want %s", notFound.SubscriberID, id)
		}
		if !errors.Is(err, broadcastor.ErrSubscriberNotFound) {
			t.Errorf("error %v does not match ErrSubscriberNotFound", err)
		}
		if !strings.Contains(err.Error(), id.String()) {
			t.Errorf("error %q does not mention the id %s", err, id)
		}
	}
}

func TestUnsubscribe_Twice(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		r, id := record(t, b)
		unsubscribe(t, b, id)

		// A repeated Unsubscribe must fail without dropping the subscription's reference again, whether it comes before
		// a Broadcast...
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("second Unsubscribe = %v, want *SubscriberNotFoundError", err)
		}
		if p := broadcast(t.Context(), b, 1); p != nil {
			t.Fatalf("Broadcast panicked: %v", p)
		}

		// ...or after one.
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("third Unsubscribe = %v, want *SubscriberNotFoundError", err)
		}
		if p := broadcast(t.Context(), b, 2); p != nil {
			t.Errorf("Broadcast after a repeated Unsubscribe panicked: %v", p)
		}

		synctest.Wait()
		if got := r.messages(); len(got) != 0 {
			t.Errorf("unsubscribed subscriber received %v, want nothing", got)
		}
	})
}

// With no Broadcast in flight, Unsubscribe closes the channel right away, and it is not sent to anymore.
func TestUnsubscribe_StopsDelivery(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		gone, goneID := record(t, b)
		stay, stayID := record(t, b)

		unsubscribe(t, b, goneID)
		b.Broadcast(t.Context(), 1) // panics if gone's channel is closed but still sent to

		unsubscribeAll(t, b, stayID)
		synctest.Wait()
		if got := gone.messages(); len(got) != 0 {
			t.Errorf("unsubscribed subscriber received %v, want nothing", got)
		}
		if got, want := stay.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("remaining subscriber received %v, want %v", got, want)
		}
	})
}

// A subscriber stuck in handle holds Broadcast up only until ctx is done, and the messages Broadcast gave up on are
// dropped rather than delivered late.
func TestBroadcast_ContextUnblocksStuckSubscriber(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck, stuckID := hold(t, b)
		b.Broadcast(t.Context(), 0) // stuck is now processing 0 and not reading

		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		b.Broadcast(cancelled, 1) // deadlocks if the cancelled ctx is ignored

		reader, readerID := record(t, b)

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		b.Broadcast(ctx, 2)
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("Broadcast returned after %v, want it to give up at the %v deadline", elapsed, time.Second)
		}

		stuck.release()
		unsubscribeAll(t, b, stuckID, readerID)
		synctest.Wait()
		if got, want := stuck.messages(), []int{0}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber received %v, want %v", got, want)
		}
		// The reader is visited before or after the deadline depending on map order, and once ctx is done select picks
		// at random between sending and giving up.
		if got := reader.messages(); len(got) != 0 && !slices.Equal(got, []int{2}) {
			t.Errorf("reader received %v, want [] or [2]", got)
		}
	})
}

// A subscriber is unsubscribed while it is stuck in handle and a Broadcast is waiting to send to it. That Broadcast
// can only give up once ctx is done, and it must still close the channel on its way out, or the subscriber's goroutine
// never ends once handle returns.
func TestBroadcast_ContextDoneAfterUnsubscribe(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck, id := hold(t, b)
		b.Broadcast(t.Context(), 0) // stuck is now processing 0 and not reading

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Broadcast(ctx, 1)
		}()
		synctest.Wait() // the Broadcast of 1 is waiting on stuck

		unsubscribe(t, b, id)
		waitClosed(t, done, "Broadcast to an unsubscribed stuck subscriber")
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("Broadcast returned after %v, want it to give up at the %v deadline", elapsed, time.Second)
		}

		stuck.release()
		synctest.Wait()
		if got, want := stuck.messages(), []int{0}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber received %v, want %v", got, want)
		}
	})
}

// handle unsubscribes its own subscription while a Broadcast is waiting to send to it. The subscriber only reads
// again once handle returns, so Unsubscribe must not wait for that Broadcast. The Broadcast then delivers its message
// and closes the channel.
func TestUnsubscribe_SelfDuringBroadcast(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		b := broadcastor.NewBroadcastor[int]()
		other, otherID := record(t, b)

		self := &recorder[int]{}
		proceed := make(chan struct{})
		var selfID uuid.UUID
		selfID = subscribe(t, b, func(ctx context.Context, msg int) error {
			self.record(msg)
			if msg == 1 {
				<-proceed
				if err := b.Unsubscribe(ctx, selfID); err != nil {
					t.Errorf("Unsubscribe: %v", err)
				}
			}

			return nil
		})

		b.Broadcast(ctx, 1) // self is now processing 1 and not reading
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Broadcast(ctx, 2)
		}()
		synctest.Wait() // the Broadcast of 2 is waiting on self
		close(proceed)
		waitClosed(t, done, "Broadcast during which the subscriber unsubscribed itself")

		b.Broadcast(ctx, 3)
		unsubscribeAll(t, b, otherID)
		synctest.Wait()

		if got, want := self.messages(), []int{1, 2}; !slices.Equal(got, want) {
			t.Errorf("unsubscribed subscriber received %v, want %v", got, want)
		}
		if got, want := other.messages(), []int{1, 2, 3}; !slices.Equal(got, want) {
			t.Errorf("other subscriber received %v, want %v", got, want)
		}
	})
}

// Unsubscribing a subscriber while a Broadcast is stuck on another one: the victim gets that Broadcast's message or not
// depending on map order, and its channel is closed once that Broadcast is done with it.
func TestUnsubscribe_OtherSubscriberDuringBroadcast(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		b := broadcastor.NewBroadcastor[int]()
		gate, gateID := hold(t, b)
		victim, victimID := record(t, b)
		b.Broadcast(ctx, 0) // gate is now processing 0 and not reading

		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Broadcast(ctx, 1)
		}()
		synctest.Wait() // the Broadcast of 1 is waiting on gate, with victim either already served or still to come

		unsubscribe(t, b, victimID)
		gate.release()
		waitClosed(t, done, "Broadcast of 1")

		b.Broadcast(ctx, 2)
		unsubscribeAll(t, b, gateID)
		synctest.Wait()

		if got := victim.messages(); !slices.Equal(got, []int{0}) && !slices.Equal(got, []int{0, 1}) {
			t.Errorf("victim received %v, want [0] or [0 1]", got)
		}
		if got, want := gate.messages(), []int{0, 1, 2}; !slices.Equal(got, want) {
			t.Errorf("gate received %v, want %v", got, want)
		}
	})
}

// A subscriber added while a Broadcast is running gets that Broadcast's message or not, and everything broadcast
// afterwards.
func TestSubscribe_DuringBroadcast(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		b := broadcastor.NewBroadcastor[int]()
		gate, gateID := hold(t, b)
		b.Broadcast(ctx, 0) // gate is now processing 0 and not reading

		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Broadcast(ctx, 1)
		}()
		synctest.Wait() // the Broadcast of 1 is waiting on gate

		late, lateID := record(t, b)
		gate.release()
		waitClosed(t, done, "Broadcast of 1")

		b.Broadcast(ctx, 2)
		unsubscribeAll(t, b, gateID, lateID)
		synctest.Wait()

		if got, want := gate.messages(), []int{0, 1, 2}; !slices.Equal(got, want) {
			t.Errorf("gate received %v, want %v", got, want)
		}
		if got := late.messages(); !slices.Equal(got, []int{2}) && !slices.Equal(got, []int{1, 2}) {
			t.Errorf("late subscriber received %v, want [2] or [1 2]", got)
		}
	})
}

// With a buffer, Broadcast only waits for a stuck subscriber once its buffer is full, and whatever is still buffered
// when it unsubscribes is processed before its channel is done.
func TestWithSubscriberBuffer(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		const buffer = 3
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		id := subscribe(t, b, stuck.handle, broadcastor.WithSubscriberBuffer[int](buffer))
		b.Broadcast(t.Context(), 0)
		synctest.Wait() // stuck is now processing 0 and not reading

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		for msg := 1; msg <= buffer; msg++ {
			b.Broadcast(ctx, msg)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Broadcasts that fit in the buffer returned after %v, want no wait", elapsed)
		}
		b.Broadcast(ctx, buffer+1)
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("Broadcast to a full buffer returned after %v, want it to give up at the %v deadline",
				elapsed, time.Second)
		}

		unsubscribe(t, b, id)
		stuck.release()
		synctest.Wait()
		if got, want := stuck.messages(), []int{0, 1, 2, 3}; !slices.Equal(got, want) {
			t.Errorf("buffered subscriber received %v, want %v", got, want)
		}
	})
}

// An async Broadcast returns without waiting for a stuck subscriber, and reaches the other subscribers without waiting
// for it either. It takes its references before returning, so subscribers that unsubscribe right after still get the
// message.
func TestWithMessageAsync(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck, stuckID := hold(t, b)
		reader, readerID := record(t, b)
		b.Broadcast(t.Context(), 0) // stuck is now processing 0 and not reading

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		b.Broadcast(ctx, 1, broadcastor.WithMessageAsync[int]())
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("async Broadcast returned after %v, want no wait", elapsed)
		}
		unsubscribeAll(t, b, stuckID, readerID)

		synctest.Wait()
		if got, want := reader.messages(), []int{0, 1}; !slices.Equal(got, want) {
			t.Errorf("reader received %v while stuck was still processing, want %v", got, want)
		}

		stuck.release()
		synctest.Wait()
		if got, want := stuck.messages(), []int{0, 1}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber received %v, want %v", got, want)
		}
	})
}

// An async Broadcast gives up on a stuck subscriber once ctx is done, drops the message and tells the subscriber's error
// handler.
func TestWithMessageAsync_ContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		errs := &recorder[error]{}
		id := subscribe(t, b, stuck.handle, broadcastor.WithSubscriberErrorHandler[int](func(_ context.Context, err error) {
			errs.record(err)
		}))
		b.Broadcast(t.Context(), 0)
		synctest.Wait() // stuck is now processing 0 and not reading

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		b.Broadcast(ctx, 1, broadcastor.WithMessageAsync[int]())
		time.Sleep(time.Second)
		synctest.Wait() // the Broadcast has given up on stuck

		got := errs.messages()
		var timeout *broadcastor.TimeoutError[int]
		if len(got) != 1 || !errors.As(got[0], &timeout) || timeout.SubscriberID != id || timeout.Message != 1 ||
			!errors.Is(timeout, context.DeadlineExceeded) {
			t.Errorf("error handler got %v, want a *TimeoutError for subscriber %s and message 1 wrapping %v",
				got, id, context.DeadlineExceeded)
		}

		unsubscribe(t, b, id)
		stuck.release()
		synctest.Wait()
		if got, want := stuck.messages(), []int{0}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber received %v, want %v", got, want)
		}
	})
}

// A message's error handler is given the errors about that message only, with the ctx passed to Subscribe, and the
// failing subscriber's own error handler still gets them too.
func TestWithMessageErrorHandler(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		ctx := context.WithValue(t.Context(), key{}, "subscribe")
		b := broadcastor.NewBroadcastor[int]()
		subscriberErrs := &recorder[int]{}
		failingID, err := b.Subscribe(ctx, func(_ context.Context, msg int) error {
			return handleError(msg)
		}, broadcastor.WithSubscriberErrorHandler[int](recordFailures(t, subscriberErrs)))
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		_, okID := record(t, b)

		messageErrs := &recorder[error]{}
		b.Broadcast(t.Context(), 1, broadcastor.WithMessageErrorHandler[int](func(ctx context.Context, err error) {
			if v := ctx.Value(key{}); v != "subscribe" {
				t.Errorf("message error handler got a ctx with value %v, want the one passed to Subscribe", v)
			}
			messageErrs.record(err)
		}))
		b.Broadcast(t.Context(), 2)
		unsubscribeAll(t, b, failingID, okID)
		synctest.Wait()

		got := messageErrs.messages()
		var handleErr *broadcastor.HandleError[int]
		if len(got) != 1 || !errors.As(got[0], &handleErr) || handleErr.SubscriberID != failingID ||
			handleErr.Message != 1 || !errors.Is(handleErr, handleError(1)) {
			t.Errorf("message error handler got %v, want a single *HandleError for subscriber %s and message 1 wrapping %v",
				got, failingID, handleError(1))
		}
		if got, want := subscriberErrs.messages(), []int{1, 2}; !slices.Equal(got, want) {
			t.Errorf("subscriber error handler got errors for messages %v, want %v", got, want)
		}
	})
}

// A message's error handler is told when Broadcast gives up on a stuck subscriber, even one without an error handler of
// its own.
func TestWithMessageErrorHandler_ContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck, id := hold(t, b)
		b.Broadcast(t.Context(), 0) // stuck is now processing 0 and not reading

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		errs := &recorder[error]{}
		b.Broadcast(ctx, 1, broadcastor.WithMessageErrorHandler[int](func(_ context.Context, err error) {
			errs.record(err)
		}))

		got := errs.messages()
		var timeout *broadcastor.TimeoutError[int]
		if len(got) != 1 || !errors.As(got[0], &timeout) || timeout.SubscriberID != id || timeout.Message != 1 ||
			!errors.Is(timeout, context.DeadlineExceeded) {
			t.Errorf("message error handler got %v, want a *TimeoutError for subscriber %s and message 1 wrapping %v",
				got, id, context.DeadlineExceeded)
		}

		unsubscribe(t, b, id)
		stuck.release()
		synctest.Wait()
		if got, want := stuck.messages(), []int{0}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber received %v, want %v", got, want)
		}
	})
}

// A subscriber whose default message options include WithMessageAsync is sent every message from its own goroutine, so
// a synchronous Broadcast does not wait for it, while the other subscribers still get the message before it returns.
func TestWithSubscriberDefaultMessageOptions_Async(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		stuckID := subscribe(t, b, stuck.handle,
			broadcastor.WithSubscriberDefaultMessageOptions(broadcastor.WithMessageAsync[int]()))
		reader, readerID := record(t, b)
		b.Broadcast(t.Context(), 0)
		synctest.Wait() // stuck is now processing 0 and not reading

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		b.Broadcast(ctx, 1)
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Broadcast returned after %v, want no wait for the async subscriber", elapsed)
		}
		synctest.Wait()
		if got, want := reader.messages(), []int{0, 1}; !slices.Equal(got, want) {
			t.Errorf("reader received %v while stuck was still processing, want %v", got, want)
		}

		unsubscribeAll(t, b, stuckID, readerID)
		stuck.release()
		synctest.Wait()
		if got, want := stuck.messages(), []int{0, 1}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber received %v, want %v", got, want)
		}
	})
}

// A default message error handler gets the errors of every message, except those of a Broadcast that passes its own
// error handler, which replaces it for that message.
func TestWithSubscriberDefaultMessageOptions_ErrorHandlerOverridden(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		defaults, override := &recorder[int]{}, &recorder[int]{}
		id := subscribe(t, b, func(_ context.Context, msg int) error {
			return handleError(msg)
		}, broadcastor.WithSubscriberDefaultMessageOptions(
			broadcastor.WithMessageErrorHandler[int](recordFailures(t, defaults))))

		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2, broadcastor.WithMessageErrorHandler[int](recordFailures(t, override)))
		b.Broadcast(t.Context(), 3)
		unsubscribe(t, b, id)
		synctest.Wait()

		if got, want := defaults.messages(), []int{1, 3}; !slices.Equal(got, want) {
			t.Errorf("default message error handler got errors for messages %v, want %v", got, want)
		}
		if got, want := override.messages(), []int{2}; !slices.Equal(got, want) {
			t.Errorf("Broadcast's message error handler got errors for messages %v, want %v", got, want)
		}
	})
}

// A message's timeout bounds how long Broadcast waits for each subscriber separately, unlike a ctx deadline, which a
// synchronous Broadcast uses up across all of them. The error handler is given the Broadcast's ctx, which the timeout
// has not ended.
func TestWithMessageTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		first, firstID := hold(t, b)
		second, secondID := hold(t, b)
		b.Broadcast(t.Context(), 0) // both are now processing 0 and not reading

		timeouts := &recorder[int]{}
		recordTimeout := recordTimeouts(t, timeouts)
		start := time.Now()
		b.Broadcast(t.Context(), 1, broadcastor.WithMessageTimeout[int](time.Second),
			broadcastor.WithMessageErrorHandler[int](func(ctx context.Context, err error) {
				if ctx.Err() != nil {
					t.Errorf("error handler got a ctx that is done (%v), want the Broadcast's", ctx.Err())
				}
				recordTimeout(ctx, err)
			}))
		if elapsed := time.Since(start); elapsed != 2*time.Second {
			t.Errorf("Broadcast returned after %v, want %v: the whole timeout for each subscriber", elapsed, 2*time.Second)
		}
		if got, want := timeouts.messages(), []int{1, 1}; !slices.Equal(got, want) {
			t.Errorf("error handler got timeouts for messages %v, want %v", got, want)
		}

		unsubscribeAll(t, b, firstID, secondID)
		first.release()
		second.release()
		synctest.Wait()
		for i, r := range []*recorder[int]{first, second} {
			if got, want := r.messages(), []int{0}; !slices.Equal(got, want) {
				t.Errorf("stuck subscriber %d received %v, want %v", i, got, want)
			}
		}
	})
}

// A subscriber's timeout bounds how long every Broadcast waits for it, unless the Broadcast passes a timeout of its own,
// longer or shorter, or 0 for none.
func TestWithSubscriberTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		timeouts := &recorder[int]{}
		id := subscribe(t, b, stuck.handle, broadcastor.WithSubscriberTimeout[int](time.Second),
			broadcastor.WithSubscriberErrorHandler[int](recordTimeouts(t, timeouts)))
		b.Broadcast(t.Context(), 0) // stuck is now processing 0 and not reading

		start := time.Now()
		b.Broadcast(t.Context(), 1)
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("Broadcast returned after %v, want it to give up at the subscriber's %v timeout",
				elapsed, time.Second)
		}

		start = time.Now()
		b.Broadcast(t.Context(), 2, broadcastor.WithMessageTimeout[int](3*time.Second))
		if elapsed := time.Since(start); elapsed != 3*time.Second {
			t.Errorf("Broadcast returned after %v, want it to give up at the message's %v timeout",
				elapsed, 3*time.Second)
		}

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		start = time.Now()
		b.Broadcast(ctx, 3, broadcastor.WithMessageTimeout[int](0))
		if elapsed := time.Since(start); elapsed != 5*time.Second {
			t.Errorf("Broadcast with no timeout returned after %v, want it to give up at the %v ctx deadline",
				elapsed, 5*time.Second)
		}

		if got, want := timeouts.messages(), []int{1, 2, 3}; !slices.Equal(got, want) {
			t.Errorf("error handler got timeouts for messages %v, want %v", got, want)
		}

		unsubscribe(t, b, id)
		stuck.release()
		synctest.Wait()
		if got, want := stuck.messages(), []int{0}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber received %v, want %v", got, want)
		}
	})
}

// Several goroutines broadcasting at once to a fixed set of subscribers: everyone gets every message, and each
// broadcaster's messages arrive in the order it sent them.
func TestBroadcast_Concurrent(t *testing.T) {
	t.Parallel()

	const broadcasters, subscribers, perBroadcaster = 4, 8, 50
	type msg struct{ from, seq int }

	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		b := broadcastor.NewBroadcastor[msg]()
		ids := make([]uuid.UUID, subscribers)
		recorders := make([]*recorder[msg], subscribers)
		for i := range subscribers {
			recorders[i], ids[i] = record(t, b)
		}

		var wg sync.WaitGroup
		for from := range broadcasters {
			wg.Go(func() {
				for seq := range perBroadcaster {
					if p := broadcast(ctx, b, msg{from, seq}); p != nil {
						t.Errorf("Broadcast panicked: %v", p)

						return
					}
				}
			})
		}
		waitGroup(t, &wg, "concurrent Broadcasts")
		unsubscribeAll(t, b, ids...)
		synctest.Wait()

		for i, r := range recorders {
			next := make([]int, broadcasters)
			for _, m := range r.messages() {
				if m.seq != next[m.from] {
					t.Errorf("subscriber %d: got message %d from broadcaster %d, want %d", i, m.seq, m.from, next[m.from])

					break
				}
				next[m.from]++
			}
			for from, n := range next {
				if n != perBroadcaster {
					t.Errorf("subscriber %d: got %d messages from broadcaster %d, want %d", i, n, from, perBroadcaster)
				}
			}
		}
	})
}

// Two goroutines broadcasting at once, with an Unsubscribe in between: neither the Unsubscribe nor the second Broadcast
// may close a channel that the first Broadcast is still sending on.
func TestBroadcast_ConcurrentWithUnsubscribe(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		b := broadcastor.NewBroadcastor[int]()
		stuck, id := hold(t, b)
		b.Broadcast(ctx, 0) // stuck is now processing 0 and not reading

		first := make(chan any, 1)
		go func() { first <- broadcast(ctx, b, 1) }()
		synctest.Wait() // the first Broadcast is now blocked sending 1 to stuck

		unsubscribe(t, b, id)
		second := make(chan any, 1)
		go func() { second <- broadcast(ctx, b, 2) }()
		synctest.Wait() // let the second Broadcast get as far as it can before stuck reads 1

		stuck.release()
		if p := <-first; p != nil {
			t.Errorf("first Broadcast panicked: %v", p)
		}
		if p := <-second; p != nil {
			t.Errorf("second Broadcast panicked: %v", p)
		}
		synctest.Wait()
		if got, want := stuck.messages(), []int{0, 1}; !slices.Equal(got, want) {
			t.Errorf("subscriber received %v, want %v", got, want)
		}
	})
}

// A Broadcast that picks a channel up from the map just as it is unsubscribed must either send to it before it is
// closed or skip it, never send to it after. The window is a few instructions wide, so this takes many rounds to hit.
func TestUnsubscribe_RacesBroadcastPickingUpChannel(t *testing.T) {
	t.Parallel()

	const iterations = 20000

	for i := range iterations {
		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			b := broadcastor.NewBroadcastor[int]()
			r, id := record(t, b)

			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Go(func() {
				<-start
				if p := broadcast(ctx, b, 1); p != nil {
					t.Errorf("iteration %d: Broadcast panicked: %v", i, p)
				}
			})
			wg.Go(func() {
				<-start
				if err := b.Unsubscribe(ctx, id); err != nil {
					t.Errorf("iteration %d: Unsubscribe(%s): %v", i, id, err)
				}
				if p := broadcast(ctx, b, 2); p != nil {
					t.Errorf("iteration %d: Broadcast after Unsubscribe panicked: %v", i, p)
				}
			})
			close(start)
			waitGroup(t, &wg, "Broadcast racing Unsubscribe")

			synctest.Wait()
			if got := r.messages(); len(got) != 0 && !slices.Equal(got, []int{1}) {
				t.Errorf("iteration %d: subscriber received %v, want [] or [1]", i, got)
			}
		})
	}
}

// handle leaves both of its consumer's subscriptions while a Broadcast is waiting to send to the first one, then
// the subscriber drains that Broadcast's message. The subscriber is not reading while handle runs, so Unsubscribe must
// never wait on a Broadcast, not even indirectly through a lock held by another Broadcast that is waiting on this one.
// This runs in real time rather than in a synctest bubble: a goroutine stuck on a sync.Mutex is not durably blocked, so
// the bubble would hang instead of failing.
func TestUnsubscribe_SeveralThenDrain(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	b := broadcastor.NewBroadcastor[int]()

	proceed := make(chan struct{})
	handleDone := make(chan struct{})
	secondBroadcastDone := make(chan struct{})
	var firstID, secondID uuid.UUID
	firstID = subscribe(t, b, func(ctx context.Context, msg int) error {
		if msg != 1 {
			return nil
		}
		defer close(handleDone)
		<-proceed
		if err := b.Unsubscribe(ctx, firstID); err != nil {
			t.Errorf("Unsubscribe(first): %v", err)
		}
		go func() {
			defer close(secondBroadcastDone)
			b.Broadcast(ctx, 3)
		}()
		time.Sleep(10 * time.Millisecond) // let the second Broadcast get as far as it can
		if err := b.Unsubscribe(ctx, secondID); err != nil {
			t.Errorf("Unsubscribe(second): %v", err)
		}

		return nil
	})
	b.Broadcast(ctx, 1) // first is now processing 1 and not reading
	_, secondID = record(t, b)

	firstBroadcastDone := make(chan struct{})
	go func() {
		defer close(firstBroadcastDone)
		b.Broadcast(ctx, 2)
	}()
	time.Sleep(10 * time.Millisecond) // let the first Broadcast get to first and wait on it
	close(proceed)

	waitClosed(t, handleDone, "handle to leave both subscriptions")
	waitClosed(t, firstBroadcastDone, "first Broadcast")
	waitClosed(t, secondBroadcastDone, "second Broadcast")
}

// Several goroutines unsubscribing the same subscriber at once, while a Broadcast runs: exactly one call must succeed,
// since each success drops the subscription's reference and there is only one to drop.
func TestUnsubscribe_ParallelSameID(t *testing.T) {
	t.Parallel()

	const iterations, callers = 2000, 8

	for i := range iterations {
		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			b := broadcastor.NewBroadcastor[int]()
			_, id := record(t, b)

			var succeeded atomic.Int32
			start := make(chan struct{})
			var wg sync.WaitGroup
			for range callers {
				wg.Go(func() {
					<-start
					switch err := b.Unsubscribe(ctx, id); {
					case err == nil:
						succeeded.Add(1)
					case !isNotFound(err):
						t.Errorf("Unsubscribe = %v, want nil or *SubscriberNotFoundError", err)
					}
				})
			}
			wg.Go(func() {
				<-start
				if p := broadcast(ctx, b, 1); p != nil {
					t.Errorf("Broadcast during parallel Unsubscribe panicked: %v", p)
				}
			})
			close(start)
			waitGroup(t, &wg, "parallel Unsubscribe")

			if p := broadcast(ctx, b, 2); p != nil {
				t.Fatalf("iteration %d: Broadcast after parallel Unsubscribe panicked: %v", i, p)
			}
			if n := succeeded.Load(); n != 1 {
				t.Fatalf("iteration %d: %d of %d parallel Unsubscribe calls succeeded, want exactly 1", i, n, callers)
			}
		})
	}
}

func TestUnsubscribe_ParallelDistinctIDs(t *testing.T) {
	t.Parallel()

	const subscribers = 100

	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		b := broadcastor.NewBroadcastor[int]()
		ids := make([]uuid.UUID, subscribers)
		recorders := make([]*recorder[int], subscribers)
		for i := range subscribers {
			recorders[i], ids[i] = record(t, b)
		}

		var wg sync.WaitGroup
		for _, id := range ids {
			wg.Go(func() {
				if err := b.Unsubscribe(ctx, id); err != nil {
					t.Errorf("Unsubscribe(%s): %v", id, err)
				}
			})
		}
		waitGroup(t, &wg, "parallel Unsubscribe")

		b.Broadcast(ctx, 1)
		synctest.Wait()
		for i, r := range recorders {
			if got := r.messages(); len(got) != 0 {
				t.Errorf("subscriber %d received %v after Unsubscribe, want nothing", i, got)
			}
		}
	})
}

func TestSubscribe_Parallel(t *testing.T) {
	t.Parallel()

	const subscribers = 100

	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		b := broadcastor.NewBroadcastor[int]()
		var mu sync.Mutex
		recorders := make(map[uuid.UUID]*recorder[int], subscribers)
		var wg sync.WaitGroup
		for range subscribers {
			wg.Go(func() {
				r := &recorder[int]{}
				id, err := b.Subscribe(ctx, r.handle)
				if err != nil {
					t.Errorf("Subscribe: %v", err)

					return
				}

				mu.Lock()
				defer mu.Unlock()
				if _, dup := recorders[id]; dup {
					t.Errorf("Subscribe returned id %s twice", id)
				}
				recorders[id] = r
			})
		}
		waitGroup(t, &wg, "parallel Subscribe")

		b.Broadcast(ctx, 1)
		unsubscribeAll(t, b, slices.Collect(maps.Keys(recorders))...)
		synctest.Wait()
		for id, r := range recorders {
			if got, want := r.messages(), []int{1}; !slices.Equal(got, want) {
				t.Errorf("subscriber %s received %v, want %v", id, got, want)
			}
		}
	})
}

// Subscribers come and go while a single goroutine broadcasts increasing numbers continuously. Every subscription must
// receive a run of consecutive numbers, in order and without gaps, and its channel must be closed some time after it
// unsubscribes.
func TestSubscribeUnsubscribe_ChurnDuringBroadcasts(t *testing.T) {
	t.Parallel()

	const consumers, rounds = 16, 30

	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		b := broadcastor.NewBroadcastor[int]()

		stop := make(chan struct{})
		broadcasterDone := make(chan struct{})
		go func() {
			defer close(broadcasterDone)
			for seq := 0; ; seq++ {
				select {
				case <-stop:
					return
				default:
				}
				if p := broadcast(ctx, b, seq); p != nil {
					t.Errorf("Broadcast panicked: %v", p)

					return
				}
			}
		}()

		var mu sync.Mutex
		recorders := make(map[uuid.UUID]*recorder[int], consumers*rounds)
		var wg sync.WaitGroup
		for range consumers {
			wg.Go(func() {
				for round := range rounds {
					// Wait for 0, 1 or 2 messages, then leave without waiting for the subscriber to finish.
					want := round % 3
					r := &recorder[int]{}
					reached := make(chan struct{})
					id, err := b.Subscribe(ctx, func(_ context.Context, msg int) error {
						if r.record(msg) == want {
							close(reached)
						}

						return nil
					})
					if err != nil {
						t.Errorf("Subscribe: %v", err)

						return
					}
					if want > 0 {
						<-reached
					}
					if err := b.Unsubscribe(ctx, id); err != nil {
						t.Errorf("Unsubscribe(%s): %v", id, err)

						return
					}

					mu.Lock()
					recorders[id] = r
					mu.Unlock()
				}
			})
		}
		waitGroup(t, &wg, "consumers")
		close(stop)
		waitClosed(t, broadcasterDone, "broadcaster")
		synctest.Wait()

		for id, r := range recorders {
			if err := checkConsecutive(r.messages()); err != nil {
				t.Errorf("subscription %s: %v", id, err)
			}
		}
	})
}

// Each typed error matches its own sentinel and no other, so errors.Is can tell them apart without knowing T, and still
// matches whatever it wraps.
func TestErrors_Is(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	targets := []error{
		broadcastor.ErrSubscriberNotFound, broadcastor.ErrSubscriberClosed, broadcastor.ErrTimeout,
		broadcastor.ErrDropped, broadcastor.ErrPanic, context.DeadlineExceeded, handleError(1),
	}
	for _, tc := range []struct {
		err     error
		matches []error
	}{
		{&broadcastor.SubscriberNotFoundError{SubscriberID: id}, []error{broadcastor.ErrSubscriberNotFound}},
		{&broadcastor.SubscriberClosedError[int]{SubscriberID: id, Message: 1}, []error{broadcastor.ErrSubscriberClosed}},
		{
			&broadcastor.TimeoutError[int]{SubscriberID: id, Message: 1, Err: context.DeadlineExceeded},
			[]error{broadcastor.ErrTimeout, context.DeadlineExceeded},
		},
		{&broadcastor.DroppedError[int]{SubscriberID: id, Message: 1}, []error{broadcastor.ErrDropped}},
		{
			&broadcastor.PanicError[int]{SubscriberID: id, Message: 1, Value: handleError(1)},
			[]error{broadcastor.ErrPanic, handleError(1)},
		},
		{&broadcastor.PanicError[int]{SubscriberID: id, Message: 1, Value: "not an error"}, []error{broadcastor.ErrPanic}},
		{&broadcastor.HandleError[int]{SubscriberID: id, Message: 1, Err: handleError(1)}, []error{handleError(1)}},
	} {
		for _, target := range targets {
			if got, want := errors.Is(tc.err, target), slices.Contains(tc.matches, target); got != want {
				t.Errorf("errors.Is(%v, %v) = %t, want %t", tc.err, target, got, want)
			}
		}
		if s := tc.err.Error(); !strings.Contains(s, id.String()) {
			t.Errorf("error %q does not mention the subscriber %s", s, id)
		}
	}
}

// A Broadcast passing WithMessageSync waits for subscribers whose default is WithMessageAsync or WithMessageNonBlocking,
// as if they had none.
func TestWithMessageSync(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		timeouts := &recorder[int]{}
		defaults := []broadcastor.MessageOptions[int]{
			broadcastor.WithMessageAsync[int](), broadcastor.WithMessageNonBlocking[int](),
		}
		ids := make([]uuid.UUID, 0, len(defaults))
		stuck := make([]*recorder[int], 0, len(defaults))
		for _, delivery := range defaults {
			r := &recorder[int]{hold: make(chan struct{})}
			ids = append(ids, subscribe(t, b, r.handle,
				broadcastor.WithSubscriberDefaultMessageOptions(delivery),
				broadcastor.WithSubscriberErrorHandler[int](recordTimeouts(t, timeouts))))
			stuck = append(stuck, r)
		}
		synctest.Wait() // both are idle, so that the non-blocking one takes 0
		b.Broadcast(t.Context(), 0)
		synctest.Wait() // both are now processing 0 and not reading

		start := time.Now()
		n := b.Broadcast(t.Context(), 1, broadcastor.WithMessageSync[int](), broadcastor.WithMessageTimeout[int](time.Second))
		if elapsed := time.Since(start); elapsed != 2*time.Second {
			t.Errorf("sync Broadcast returned after %v, want %v: the whole timeout for each subscriber", elapsed, 2*time.Second)
		}
		if n != 0 {
			t.Errorf("Broadcast handed the message to %d subscribers, want 0", n)
		}
		if got, want := timeouts.messages(), []int{1, 1}; !slices.Equal(got, want) {
			t.Errorf("error handler got timeouts for messages %v, want %v", got, want)
		}

		unsubscribeAll(t, b, ids...)
		for _, r := range stuck {
			r.release()
		}
		synctest.Wait()
		for i, r := range stuck {
			if got, want := r.messages(), []int{0}; !slices.Equal(got, want) {
				t.Errorf("stuck subscriber %d received %v, want %v", i, got, want)
			}
		}
	})
}

// Broadcast returns how many subscribers it handed the message to: those that took it when it waits for them, and every
// one it started sending to when it does not.
func TestBroadcast_Count(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck, stuckID := hold(t, b)
		_, firstID := record(t, b)
		_, secondID := record(t, b)
		if n := b.Broadcast(t.Context(), 0); n != 3 { // stuck is now processing 0 and not reading
			t.Errorf("Broadcast to idle subscribers handed the message to %d, want 3", n)
		}

		if n := b.Broadcast(t.Context(), 1, broadcastor.WithMessageTimeout[int](time.Second)); n != 2 {
			t.Errorf("sync Broadcast handed the message to %d subscribers, want 2: all but the stuck one", n)
		}
		if n := b.Broadcast(t.Context(), 2, broadcastor.WithMessageAsync[int]()); n != 3 {
			t.Errorf("async Broadcast handed the message to %d subscribers, want 3: all of them, stuck or not", n)
		}

		unsubscribeAll(t, b, stuckID, firstID, secondID)
		if n := b.Broadcast(t.Context(), 3); n != 0 {
			t.Errorf("Broadcast with no subscribers handed the message to %d, want 0", n)
		}

		stuck.release()
		synctest.Wait()
		if got, want := stuck.messages(), []int{0, 2}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber received %v, want %v", got, want)
		}
	})
}

// A non-blocking Broadcast never waits: it hands the message to the subscribers that are idle, and drops it for those
// still busy in handle, telling their error handlers.
func TestWithMessageNonBlocking(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		stuck, stuckID := hold(t, b)
		reader, readerID := record(t, b)
		b.Broadcast(t.Context(), 0)
		synctest.Wait() // stuck is now processing 0 and not reading, and reader is idle

		errs := &recorder[error]{}
		start := time.Now()
		n := b.Broadcast(t.Context(), 1, broadcastor.WithMessageNonBlocking[int](),
			broadcastor.WithMessageErrorHandler[int](func(_ context.Context, err error) {
				errs.record(err)
			}))
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("non-blocking Broadcast returned after %v, want no wait", elapsed)
		}
		if n != 1 {
			t.Errorf("non-blocking Broadcast handed the message to %d subscribers, want 1: the idle one", n)
		}
		got := errs.messages()
		var dropped *broadcastor.DroppedError[int]
		if len(got) != 1 || !errors.As(got[0], &dropped) || dropped.SubscriberID != stuckID || dropped.Message != 1 ||
			!errors.Is(got[0], broadcastor.ErrDropped) {
			t.Errorf("message error handler got %v, want a single *DroppedError for subscriber %s and message 1",
				got, stuckID)
		}

		unsubscribeAll(t, b, stuckID, readerID)
		stuck.release()
		synctest.Wait()
		if got, want := stuck.messages(), []int{0}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber received %v, want %v", got, want)
		}
		if got, want := reader.messages(), []int{0, 1}; !slices.Equal(got, want) {
			t.Errorf("reader received %v, want %v", got, want)
		}
	})
}

// A non-blocking Broadcast fills a busy subscriber's buffer, and drops the message once it is full.
func TestWithMessageNonBlocking_Buffer(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		const buffer = 2
		b := broadcastor.NewBroadcastor[int]()
		stuck := &recorder[int]{hold: make(chan struct{})}
		dropped := &recorder[int]{}
		id := subscribe(t, b, stuck.handle, broadcastor.WithSubscriberBuffer[int](buffer),
			broadcastor.WithSubscriberDefaultMessageOptions(broadcastor.WithMessageNonBlocking[int]()),
			broadcastor.WithSubscriberErrorHandler[int](func(_ context.Context, err error) {
				var droppedErr *broadcastor.DroppedError[int]
				if !errors.As(err, &droppedErr) {
					t.Errorf("error handler got %v, want a *DroppedError", err)

					return
				}
				dropped.record(droppedErr.Message)
			}))
		b.Broadcast(t.Context(), 0)
		synctest.Wait() // stuck is now processing 0 and not reading, with an empty buffer

		for msg := 1; msg <= buffer+1; msg++ {
			want := 1
			if msg > buffer {
				want = 0
			}
			if n := b.Broadcast(t.Context(), msg); n != want {
				t.Errorf("Broadcast of %d handed it to %d subscribers, want %d", msg, n, want)
			}
		}
		if got, want := dropped.messages(), []int{buffer + 1}; !slices.Equal(got, want) {
			t.Errorf("error handler got drops for messages %v, want %v", got, want)
		}

		unsubscribe(t, b, id)
		stuck.release()
		synctest.Wait()
		if got, want := stuck.messages(), []int{0, 1, 2}; !slices.Equal(got, want) {
			t.Errorf("stuck subscriber received %v, want %v", got, want)
		}
	})
}

// With WithSubscriberRecover, a panic in handle is reported as a *PanicError, and the subscriber goes on with the next
// message.
func TestWithSubscriberRecover(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		r := &recorder[int]{}
		errs := &recorder[error]{}
		id := subscribe(t, b, func(_ context.Context, msg int) error {
			r.record(msg)
			if msg == 2 {
				panic(handleError(msg))
			}

			return nil
		}, broadcastor.WithSubscriberRecover[int](), broadcastor.WithSubscriberErrorHandler[int](func(_ context.Context, err error) {
			errs.record(err)
		}))

		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(t.Context(), msg)
		}
		unsubscribe(t, b, id)
		synctest.Wait()

		if got, want := r.messages(), []int{1, 2, 3}; !slices.Equal(got, want) {
			t.Errorf("subscriber processed %v, want %v", got, want)
		}
		got := errs.messages()
		var panicErr *broadcastor.PanicError[int]
		if len(got) != 1 || !errors.As(got[0], &panicErr) {
			t.Fatalf("error handler got %v, want a single *PanicError", got)
		}
		if panicErr.SubscriberID != id || panicErr.Message != 2 || panicErr.Value != handleError(2) {
			t.Errorf("error is for subscriber %s and message %d with value %v, want %s, 2 and %v",
				panicErr.SubscriberID, panicErr.Message, panicErr.Value, id, handleError(2))
		}
		if !errors.Is(panicErr, broadcastor.ErrPanic) || !errors.Is(panicErr, handleError(2)) {
			t.Errorf("error %v does not match both ErrPanic and the value handle panicked with", panicErr)
		}
		if !bytes.Contains(panicErr.Stack, []byte("TestWithSubscriberRecover")) {
			t.Errorf("stack does not go through handle:\n%s", panicErr.Stack)
		}
	})
}

// SubscribeSeq yields every message the subscriber takes, in order, and breaking out of the loop unsubscribes it.
func TestSubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		_, seq := subscribeSeq(t, b)

		go func() {
			for msg := 1; msg <= 3; msg++ {
				b.Broadcast(t.Context(), msg)
			}
		}()
		var got []int
		for msg := range seq {
			got = append(got, msg)
			if msg == 3 {
				break
			}
		}
		if want := []int{1, 2, 3}; !slices.Equal(got, want) {
			t.Errorf("loop got %v, want %v", got, want)
		}

		if n := b.Broadcast(t.Context(), 4); n != 0 {
			t.Errorf("Broadcast after the loop ended handed the message to %d subscribers, want 0", n)
		}
		synctest.Wait()
	})
}

// The loop body takes the place of handle: while it runs, the subscriber is busy and takes nothing, so a non-blocking
// Broadcast drops its message and a sync one waits.
func TestSubscribeSeq_BodyHoldsSubscriber(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		errs := &recorder[error]{}
		_, seq := subscribeSeq(t, b, broadcastor.WithSubscriberErrorHandler[int](func(_ context.Context, err error) {
			errs.record(err)
		}))

		yielded := &recorder[int]{}
		proceed := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for msg := range seq {
				yielded.record(msg)
				<-proceed
				if msg == 2 {
					break
				}
			}
		}()
		b.Broadcast(t.Context(), 0)
		synctest.Wait() // the loop body is now processing 0

		if n := b.Broadcast(t.Context(), 1, broadcastor.WithMessageNonBlocking[int]()); n != 0 {
			t.Errorf("non-blocking Broadcast handed the message to %d subscribers, want 0: the loop body is busy", n)
		}
		start := time.Now()
		if n := b.Broadcast(t.Context(), 1, broadcastor.WithMessageTimeout[int](time.Second)); n != 0 {
			t.Errorf("sync Broadcast handed the message to %d subscribers, want 0: the loop body is busy", n)
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("sync Broadcast returned after %v, want it to wait for the whole %v timeout", elapsed, time.Second)
		}

		close(proceed)
		if n := b.Broadcast(t.Context(), 2); n != 1 {
			t.Errorf("Broadcast handed the message to %d subscribers, want 1", n)
		}
		waitClosed(t, done, "the loop to break")
		synctest.Wait()

		if got, want := yielded.messages(), []int{0, 2}; !slices.Equal(got, want) {
			t.Errorf("loop got %v, want %v", got, want)
		}
		got := errs.messages()
		if len(got) != 2 || !errors.Is(got[0], broadcastor.ErrDropped) || !errors.Is(got[1], broadcastor.ErrTimeout) {
			t.Errorf("error handler got %v, want a *DroppedError then a *TimeoutError", got)
		}
	})
}

// Once the loop has broken, the messages the subscriber took but did not yield are reported as *SubscriberClosedError,
// with the ctx passed to SubscribeSeq, and a Broadcast that was waiting on the subscriber returns.
func TestSubscribeSeq_BreakReportsUnyielded(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		ctx := context.WithValue(t.Context(), key{}, "seq")
		b := broadcastor.NewBroadcastor[int]()
		closed := &recorder[int]{}
		id, seq, err := b.SubscribeSeq(ctx, broadcastor.WithSubscriberBuffer[int](2),
			broadcastor.WithSubscriberErrorHandler[int](func(ctx context.Context, err error) {
				var closedErr *broadcastor.SubscriberClosedError[int]
				if !errors.As(err, &closedErr) || !errors.Is(err, broadcastor.ErrSubscriberClosed) {
					t.Errorf("error handler got %v, want a *SubscriberClosedError", err)

					return
				}
				if v := ctx.Value(key{}); v != "seq" {
					t.Errorf("error handler got a ctx with value %v, want the one passed to SubscribeSeq", v)
				}
				closed.record(closedErr.Message)
			}))
		if err != nil {
			t.Fatalf("SubscribeSeq: %v", err)
		}

		// Nothing reads before the loop starts: 1 and 2 fill the buffer, and the Broadcast of 3 waits.
		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Broadcast(t.Context(), 3)
		}()
		synctest.Wait()

		for msg := range seq {
			if msg != 1 {
				t.Errorf("loop got %d first, want 1", msg)
			}

			break
		}
		waitClosed(t, done, "Broadcast to a subscriber whose loop has ended")
		synctest.Wait()

		if got, want := closed.messages(), []int{2, 3}; !slices.Equal(got, want) {
			t.Errorf("error handler got *SubscriberClosedError for messages %v, want %v", got, want)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe after the loop ended = %v, want *SubscriberNotFoundError", err)
		}
	})
}

// Once ctx is done, a loop waiting for its next message ends and unsubscribes.
func TestSubscribeSeq_ContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		_, seq, err := b.SubscribeSeq(ctx)
		if err != nil {
			t.Fatalf("SubscribeSeq: %v", err)
		}

		got := &recorder[int]{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for msg := range seq {
				got.record(msg)
			}
		}()
		b.Broadcast(t.Context(), 1)
		synctest.Wait() // the loop is waiting for its next message

		cancel()
		waitClosed(t, done, "the loop to end once ctx is done")
		if n := b.Broadcast(t.Context(), 2); n != 0 {
			t.Errorf("Broadcast after the loop ended handed the message to %d subscribers, want 0", n)
		}
		synctest.Wait()
		if got, want := got.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("loop got %v, want %v", got, want)
		}
	})
}

// A loop whose ctx is already done yields nothing, even with messages waiting, and reports them.
func TestSubscribeSeq_ContextDoneFirst(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		errs := &recorder[error]{}
		_, seq, err := b.SubscribeSeq(ctx, broadcastor.WithSubscriberBuffer[int](1),
			broadcastor.WithSubscriberErrorHandler[int](func(_ context.Context, err error) {
				errs.record(err)
			}))
		if err != nil {
			t.Fatalf("SubscribeSeq: %v", err)
		}

		b.Broadcast(t.Context(), 1)
		cancel()
		for msg := range seq {
			t.Errorf("loop got %d, want nothing once ctx is done", msg)
		}
		synctest.Wait()
		if got := errs.messages(); len(got) != 1 || !errors.Is(got[0], broadcastor.ErrSubscriberClosed) {
			t.Errorf("error handler got %v, want a single *SubscriberClosedError", got)
		}
	})
}

// Unsubscribing a SubscribeSeq subscriber from elsewhere ends its loop once it has yielded every message it took, like
// a handle subscriber processes them after Unsubscribe.
func TestSubscribeSeq_Unsubscribe(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		id, seq := subscribeSeq(t, b, broadcastor.WithSubscriberBuffer[int](2),
			broadcastor.WithSubscriberErrorHandler[int](func(_ context.Context, err error) {
				t.Errorf("error handler got %v, want no error", err)
			}))

		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		unsubscribe(t, b, id)

		var got []int
		for msg := range seq {
			got = append(got, msg)
		}
		if want := []int{1, 2}; !slices.Equal(got, want) {
			t.Errorf("loop got %v, want %v", got, want)
		}
		synctest.Wait()
	})
}

// The iterator can be ranged over once: a range after it, or alongside it, yields nothing.
func TestSubscribeSeq_RangeOnce(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		_, seq := subscribeSeq(t, b)

		done := make(chan struct{})
		go func() {
			defer close(done)
			for range seq {
				break
			}
		}()
		synctest.Wait() // the first loop is waiting for its next message

		for msg := range seq {
			t.Errorf("a second loop alongside the first got %d, want nothing", msg)
		}
		if n := b.Broadcast(t.Context(), 1); n != 1 {
			t.Errorf("Broadcast handed the message to %d subscribers, want 1", n)
		}
		waitClosed(t, done, "the first loop to break")

		for msg := range seq {
			t.Errorf("a loop after the first got %d, want nothing", msg)
		}
		synctest.Wait()
	})
}

// A panic in the loop body reaches the caller, and still unsubscribes.
func TestSubscribeSeq_Panic(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[int]()
		_, seq := subscribeSeq(t, b)

		go b.Broadcast(t.Context(), 1)
		p := recovered(func() {
			for msg := range seq {
				panic(handleError(msg))
			}
		})
		if p != handleError(1) {
			t.Errorf("loop panicked with %v, want %v", p, handleError(1))
		}
		if n := b.Broadcast(t.Context(), 2); n != 0 {
			t.Errorf("Broadcast after the loop panicked handed the message to %d subscribers, want 0", n)
		}
		synctest.Wait()
	})
}

// recorder is a handle function that records every message it is given, in order. If hold is set, handle does not
// return until it is closed, so the subscriber stops reading and holds up the next Broadcast until then.
type recorder[T any] struct {
	hold chan struct{}

	mu  sync.Mutex
	got []T
}

func (r *recorder[T]) handle(_ context.Context, msg T) error {
	r.record(msg)
	if r.hold != nil {
		<-r.hold
	}

	return nil
}

// record records msg and returns how many messages have been recorded so far.
func (r *recorder[T]) record(msg T) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, msg)

	return len(r.got)
}

// messages returns everything recorded so far, in order.
func (r *recorder[T]) messages() []T {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.got)
}

// release lets handle return, now and for every message after.
func (r *recorder[T]) release() {
	close(r.hold)
}

type handleError int

func (e handleError) Error() string {
	return fmt.Sprintf("handling %d failed", int(e))
}

// recordFailures returns an error handler that records the message of every *HandleError it is given into r, and fails
// the test on any other error.
func recordFailures(t *testing.T, r *recorder[int]) func(context.Context, error) {
	t.Helper()

	return func(_ context.Context, err error) {
		var handleErr *broadcastor.HandleError[int]
		if !errors.As(err, &handleErr) {
			t.Errorf("error handler got %v, want a *HandleError", err)

			return
		}
		r.record(handleErr.Message)
	}
}

// recordTimeouts returns an error handler that records the message of every *TimeoutError wrapping
// context.DeadlineExceeded it is given into r, and fails the test on any other error.
func recordTimeouts(t *testing.T, r *recorder[int]) func(context.Context, error) {
	t.Helper()

	return func(_ context.Context, err error) {
		var timeout *broadcastor.TimeoutError[int]
		if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error handler got %v, want a *TimeoutError wrapping %v", err, context.DeadlineExceeded)

			return
		}
		r.record(timeout.Message)
	}
}

func subscribe[T any](
	t *testing.T, b *broadcastor.Broadcastor[T], handle func(context.Context, T) error,
	options ...broadcastor.SubscriberOption[T],
) uuid.UUID {
	t.Helper()
	id, err := b.Subscribe(t.Context(), handle, options...)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	return id
}

// record subscribes a recorder.
func record[T any](t *testing.T, b *broadcastor.Broadcastor[T]) (*recorder[T], uuid.UUID) {
	t.Helper()
	r := &recorder[T]{}
	id := subscribe(t, b, r.handle)

	return r, id
}

// hold subscribes a recorder that holds on to every message until it is released.
func hold[T any](t *testing.T, b *broadcastor.Broadcastor[T]) (*recorder[T], uuid.UUID) {
	t.Helper()
	r := &recorder[T]{hold: make(chan struct{})}
	id := subscribe(t, b, r.handle)

	return r, id
}

// subscribeSeq subscribes with SubscribeSeq, with the test's ctx.
func subscribeSeq[T any](t *testing.T, b *broadcastor.Broadcastor[T], options ...broadcastor.SubscriberOption[T]) (uuid.UUID, iter.Seq[T]) {
	t.Helper()
	id, seq, err := b.SubscribeSeq(t.Context(), options...)
	if err != nil {
		t.Fatalf("SubscribeSeq: %v", err)
	}

	return id, seq
}

func unsubscribe[T any](t *testing.T, b *broadcastor.Broadcastor[T], id uuid.UUID) {
	t.Helper()
	if err := b.Unsubscribe(t.Context(), id); err != nil {
		t.Fatalf("Unsubscribe(%s): %v", id, err)
	}
}

func unsubscribeAll[T any](t *testing.T, b *broadcastor.Broadcastor[T], ids ...uuid.UUID) {
	t.Helper()
	for _, id := range ids {
		unsubscribe(t, b, id)
	}
}

// broadcast calls b.Broadcast and returns whatever it panicked with.
func broadcast[T any](ctx context.Context, b *broadcastor.Broadcastor[T], msg T) any {
	return recovered(func() { b.Broadcast(ctx, msg) })
}

// recovered runs f and returns whatever it panicked with, so that a panic fails only the test that caused it instead
// of crashing the whole test binary.
func recovered(f func()) any {
	var r any
	func() {
		defer func() { r = recover() }()
		f()
	}()

	return r
}

func isNotFound(err error) bool {
	var notFound *broadcastor.SubscriberNotFoundError

	return errors.As(err, &notFound)
}

func waitClosed(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(deadlockTimeout):
		t.Fatalf("still waiting for %s after %v: deadlock", what, deadlockTimeout)
	}
}

func waitGroup(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	waitClosed(t, done, what)
}

var errNotConsecutive = errors.New("reordered or skipped")

// checkConsecutive checks that got is a run of consecutive numbers, which is what one subscription receives while a
// single goroutine broadcasts increasing numbers.
func checkConsecutive(got []int) error {
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1]+1 {
			return fmt.Errorf("message %d followed by %d: %w: %v", got[i-1], got[i], errNotConsecutive, got)
		}
	}

	return nil
}
