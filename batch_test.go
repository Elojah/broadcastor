package broadcastor_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/holder"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
)

// A subscriber hands a batch whole to its batch handler, through the batch middlewares, and each single message to
// handle. One without a batch handler hands each value to handle, through its middlewares. Stats count values.
func TestBroadcastBatch(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		unrolled, wrapped := &recorder[int]{}, &recorder[int]{}
		unrolledID := subscribe(t, b, unrolled.handle, subscriber.WithMiddleware(recordMiddleware(wrapped)))
		singles, batches := &recorder[int]{}, &batchRecorder{}
		sent := batchOf(1, 2, 3)
		var calls atomic.Int64
		batchID := subscribe(t, b, singles.handle, subscriber.WithBatchHandler(
			func(ctx context.Context, id uuid.UUID, batch message.Batch[int]) error {
				if batch != message.Batch[int](sent) {
					t.Errorf("batch handler got %v, want the batch sent", batch)
				}

				return batches.handle(ctx, id, batch)
			},
			func(next subscriber.Handler[message.Batch[int]]) subscriber.Handler[message.Batch[int]] {
				return func(ctx context.Context, id uuid.UUID, batch message.Batch[int]) error {
					calls.Add(1)

					return next(ctx, id, batch)
				}
			}))

		if n := b.BroadcastBatch(t.Context(), sent); n != 2 {
			t.Errorf("BroadcastBatch handed the batch to %d subscribers, want 2", n)
		}
		if n := b.Broadcast(t.Context(), 4); n != 2 {
			t.Errorf("Broadcast handed the message to %d subscribers, want 2", n)
		}
		for _, empty := range []message.Batch[int]{nil, batchOf()} {
			if n := b.BroadcastBatch(t.Context(), empty); n != 0 {
				t.Errorf("BroadcastBatch(%v) handed it to %d subscribers, want none", empty, n)
			}
		}
		synctest.Wait()

		want := []int{1, 2, 3, 4}
		if got := unrolled.messages(); !slices.Equal(got, want) {
			t.Errorf("handle without a batch handler got %v, want %v", got, want)
		}
		if got := wrapped.messages(); !slices.Equal(got, want) {
			t.Errorf("its middleware got %v, want %v", got, want)
		}
		if got := singles.messages(); !slices.Equal(got, []int{4}) {
			t.Errorf("handle with a batch handler got %v, want [4]", got)
		}
		checkBatches(t, batches, [][]int{{1, 2, 3}})
		if n := calls.Load(); n != 1 {
			t.Errorf("the batch middleware ran %d times, want once", n)
		}
		checkStats(t, b, unrolledID, subscriber.Stats{Delivered: 4, Handled: 4})
		checkStats(t, b, batchID, subscriber.Stats{Delivered: 4, Handled: 4, Batches: 1})
	})
}

// Every delivery mode hands the batch whole, in one send, and BroadcastBatch counts the subscriber once.
func TestBroadcastBatch_Delivery(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		subscribe []subscriber.Option[int]
		broadcast []message.Option[int]
	}{
		{name: "sync"},
		{name: "parallel", broadcast: []message.Option[int]{message.WithParallel[int]()}},
		{name: "async", broadcast: []message.Option[int]{message.WithAsync[int]()}},
		{
			name:      "non-blocking",
			subscribe: []subscriber.Option[int]{subscriber.WithBuffer[int](1)},
			broadcast: []message.Option[int]{message.WithNonBlocking[int]()},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := newBroadcastor[int](t)
				batches := &batchRecorder{}
				id := subscribe(t, b, (&recorder[int]{}).handle, append(tt.subscribe, subscriber.WithBatchHandler(batches.handle))...)
				if n := b.BroadcastBatch(t.Context(), batchOf(1, 2), tt.broadcast...); n != 1 {
					t.Errorf("BroadcastBatch handed the batch to %d subscribers, want 1", n)
				}
				synctest.Wait()
				checkBatches(t, batches, [][]int{{1, 2}})
				checkStats(t, b, id, subscriber.Stats{Delivered: 2, Handled: 2, Batches: 1, Buffer: statsOf(t, b, id).Buffer})
			})
		})
	}
}

// A batch the subscriber cannot take is lost whole: reported once, with the batch in the error, to the subscriber's
// error handler then to the batch's, and stored one value at a time, each with that error. Stats count each value.
func TestBroadcastBatch_Lost(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		handled := &recorder[int]{hold: make(chan struct{})}
		var subscriberErrs, batchErrs []string
		store := &recordStore{}
		id := subscribe(t, b, handled.handle, subscriber.WithDeadLetters[int](store),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
				subscriberErrs = append(subscriberErrs, describeBatchLoss(t, err))
			}))
		recordBatchErrs := message.WithErrorHandler[int](func(_ context.Context, err error) {
			batchErrs = append(batchErrs, describeBatchLoss(t, err))
		})

		// handle holds on to 0, so the batches find it busy.
		b.Broadcast(t.Context(), 0)
		synctest.Wait()
		if n := b.BroadcastBatch(t.Context(), batchOf(1, 2, 3), message.WithNonBlocking[int](), recordBatchErrs); n != 0 {
			t.Errorf("non-blocking BroadcastBatch to a busy subscriber handed it over %d times, want none", n)
		}
		if n := b.BroadcastBatch(t.Context(), batchOf(4, 5), message.WithTimeout[int](time.Second), recordBatchErrs); n != 0 {
			t.Errorf("BroadcastBatch timing out handed it over %d times, want none", n)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 1, TimedOut: 2, Dropped: 3, Handling: time.Second})
		handled.release()
		synctest.Wait()

		want := []string{"dropped [1 2 3]", "timed out [4 5]"}
		if !slices.Equal(subscriberErrs, want) || !slices.Equal(batchErrs, want) {
			t.Errorf("the subscriber's error handler got %v and the batch's %v, want %v for both", subscriberErrs, batchErrs, want)
		}
		records := store.messages()
		stored := make([]int, 0, len(records))
		for _, r := range records {
			stored = append(stored, r.Message)
			if wantErr := map[bool]error{true: subscriber.ErrDropped, false: subscriber.ErrTimeout}[r.Message <= 3]; !errors.Is(r.Err, wantErr) {
				t.Errorf("record for %d has error %v, want one matching %v", r.Message, r.Err, wantErr)
			}
		}
		if wantStored := []int{1, 2, 3, 4, 5}; !slices.Equal(stored, wantStored) {
			t.Errorf("dead letters got %v, want %v", stored, wantStored)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 1, Handled: 1, TimedOut: 2, Dropped: 3, HandleTime: time.Second})
	})
}

// The batch handler gets the batch's ctx. Its error counts each value as failed, and is reported once, to the
// subscriber's error handler then to the batch's, with the batch's ctx, and stored one value at a time.
func TestSubscriberWithBatchHandler_Errors(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		type key struct{}
		var handledWith any
		var subscriberErrs, batchErrs []error
		store := &recordStore{}
		id := subscribe(t, b, (&recorder[int]{}).handle, subscriber.WithDeadLetters[int](store),
			subscriber.WithBatchHandler(func(ctx context.Context, _ uuid.UUID, batch message.Batch[int]) error {
				handledWith = ctx.Value(key{})

				return handleError(batch.Len())
			}),
			subscriber.WithErrorHandler[int](func(ctx context.Context, err error) {
				if v := ctx.Value(key{}); v != "batch" {
					t.Errorf("the subscriber's error handler got a ctx with %v, want the batch's", v)
				}
				subscriberErrs = append(subscriberErrs, err)
			}))

		b.BroadcastBatch(t.Context(), batchOf(1, 2, 3), message.WithContext[int](context.WithValue(t.Context(), key{}, "batch")),
			message.WithErrorHandler[int](func(_ context.Context, err error) { batchErrs = append(batchErrs, err) }))
		synctest.Wait()

		if handledWith != "batch" {
			t.Errorf("the batch handler got a ctx with %v, want the batch's", handledWith)
		}
		want := []error{handleError(3)}
		if !slices.Equal(subscriberErrs, want) || !slices.Equal(batchErrs, want) {
			t.Errorf("the subscriber's error handler got %v and the batch's %v, want %v for both", subscriberErrs, batchErrs, want)
		}
		wantRecords := []subscriber.Record[int]{
			{SubscriberID: id, Message: 1, Err: handleError(3)},
			{SubscriberID: id, Message: 2, Err: handleError(3)},
			{SubscriberID: id, Message: 3, Err: handleError(3)},
		}
		if got := store.messages(); !slices.Equal(got, wantRecords) {
			t.Errorf("dead letters got %v, want %v", got, wantRecords)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 3, Failed: 3, Batches: 1})
	})
}

// The batch middlewares wrap the batch handler like any handler: Recover makes its panic a *PanicError holding the
// batch.
func TestSubscriberWithBatchHandler_Recover(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		sent := batchOf(1, 2)
		var errs []error
		subscribe(t, b, (&recorder[int]{}).handle,
			subscriber.WithBatchHandler(func(context.Context, uuid.UUID, message.Batch[int]) error {
				panic("batch handler")
			}, middleware.Recover[message.Batch[int]]()),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) { errs = append(errs, err) }))

		b.BroadcastBatch(t.Context(), sent)
		synctest.Wait()

		var panicErr *subscriber.PanicError[message.Batch[int]]
		if len(errs) != 1 || !errors.As(errs[0], &panicErr) || !errors.Is(errs[0], subscriber.ErrPanic) || panicErr.Message != message.Batch[int](sent) {
			t.Errorf("error handler got %v, want one *PanicError holding the batch", errs)
		}
	})
}

// evict sees the batch handler's error once, and the eviction is reported once, holding the batch.
func TestSubscriberWithBatchHandler_Evict(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		var evicts atomic.Int64
		var errs []string
		id := subscribe(t, b, (&recorder[int]{}).handle,
			subscriber.WithBatchHandler(func(_ context.Context, _ uuid.UUID, batch message.Batch[int]) error {
				return handleError(batch.Len())
			}),
			subscriber.WithEvict[int](func(error) bool { return evicts.Add(1) > 0 }),
			subscriber.WithErrorHandler[int](func(_ context.Context, err error) { errs = append(errs, describeBatchLoss(t, err)) }))

		b.BroadcastBatch(t.Context(), batchOf(1, 2, 3))
		synctest.Wait()

		if n := evicts.Load(); n != 1 {
			t.Errorf("evict ran %d times, want once", n)
		}
		if want := []string{"evicted by handling 3 failed [1 2 3]"}; !slices.Equal(errs, want) {
			t.Errorf("error handler got %v, want %v", errs, want)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe after the eviction = %v, want *SubscriberNotFoundError", err)
		}
	})
}

// Without a batch handler, each value is handled, and fails, alone: handle's error is about that value only.
func TestBroadcastBatch_Unrolled(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		failures := &recorder[int]{}
		store := &recordStore{}
		id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			if msg == 2 {
				return handleError(msg)
			}

			return nil
		}, subscriber.WithDeadLetters[int](store), subscriber.WithErrorHandler[int](recordFailures(t, failures)))

		b.BroadcastBatch(t.Context(), batchOf(1, 2, 3))
		synctest.Wait()

		if got := failures.messages(); !slices.Equal(got, []int{2}) {
			t.Errorf("error handler got failures for %v, want [2]", got)
		}
		if got, want := store.messages(), []subscriber.Record[int]{{SubscriberID: id, Message: 2, Err: handleError(2)}}; !slices.Equal(got, want) {
			t.Errorf("dead letters got %v, want %v", got, want)
		}
		checkStats(t, b, id, subscriber.Stats{Delivered: 3, Handled: 2, Failed: 1})
	})
}

// A subscriber's filter sees each value of a batch once. Of a batch it keeps part of, the subscriber takes a batch of
// the same type holding what it keeps, of one it keeps whole, the batch itself, and of one it keeps nothing of,
// nothing, neither counted nor reported.
func TestBroadcastBatch_Filter(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		var filtered atomic.Int64
		keepEven := subscriber.WithFilter(func(msg int) bool {
			filtered.Add(1)

			return msg%2 == 0
		})
		var got []message.Batch[int]
		batchID := subscribe(t, b, (&recorder[int]{}).handle, keepEven, subscriber.WithBatchHandler(
			func(_ context.Context, _ uuid.UUID, batch message.Batch[int]) error {
				got = append(got, batch)

				return nil
			}), subscriber.WithErrorHandler[int](func(_ context.Context, err error) {
			t.Errorf("error handler got %v, want nothing", err)
		}))
		unrolled := &recorder[int]{}
		unrolledID := subscribe(t, b, unrolled.handle, subscriber.WithFilter(func(msg int) bool { return msg%2 == 0 }))

		part, whole, none := &tally{values: []int{1, 2, 3, 4}}, &tally{values: []int{6, 8}}, &tally{values: []int{5, 7}}
		for _, tt := range []struct {
			batch *tally
			want  int
		}{{part, 2}, {whole, 2}, {none, 0}} {
			if n := b.BroadcastBatch(t.Context(), tt.batch); n != tt.want {
				t.Errorf("BroadcastBatch(%v) handed it to %d subscribers, want %d", tt.batch.values, n, tt.want)
			}
		}
		synctest.Wait()

		if n := filtered.Load(); n != 8 {
			t.Errorf("the filter ran %d times, want 8, once per value", n)
		}
		if len(got) != 2 {
			t.Fatalf("batch handler got %d batches, want 2", len(got))
		}
		if kept, ok := got[0].(*tally); !ok || kept == part || !slices.Equal(kept.values, []int{2, 4}) {
			t.Errorf("batch handler got %#v first, want a new *tally holding [2 4]", got[0])
		}
		if got[1] != message.Batch[int](whole) {
			t.Errorf("batch handler got %#v second, want the batch sent", got[1])
		}
		if got, want := unrolled.messages(), []int{2, 4, 6, 8}; !slices.Equal(got, want) {
			t.Errorf("handle without a batch handler got %v, want %v", got, want)
		}
		checkStats(t, b, batchID, subscriber.Stats{Delivered: 4, Handled: 4, Batches: 2})
		checkStats(t, b, unrolledID, subscriber.Stats{Delivered: 4, Handled: 4})
	})
}

// A batch the subscriber discards while it waits in the buffer is reported whole, once. Once handed to handle one
// value at a time, what is left is reported a value at a time.
func TestBroadcastBatch_Discard(t *testing.T) {
	t.Parallel()

	t.Run("buffered", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			b := newBroadcastor[int](t)
			handled := &recorder[int]{hold: make(chan struct{})}
			var errs []string
			id := subscribe(t, b, handled.handle, subscriber.WithBuffer[int](1),
				subscriber.WithErrorHandler[int](func(_ context.Context, err error) { errs = append(errs, describeBatchLoss(t, err)) }))
			b.Broadcast(t.Context(), 0)
			synctest.Wait()
			b.BroadcastBatch(t.Context(), batchOf(1, 2, 3))
			checkStats(t, b, id, subscriber.Stats{Delivered: 4, Queued: 3, Buffer: 1})

			if err := b.Unsubscribe(t.Context(), id, subscriber.WithUnsubscribeDiscard()); err != nil {
				t.Fatalf("Unsubscribe: %v", err)
			}
			handled.release()
			synctest.Wait()

			if got := handled.messages(); !slices.Equal(got, []int{0}) {
				t.Errorf("handle got %v, want [0]", got)
			}
			if want := []string{"closed [1 2 3]"}; !slices.Equal(errs, want) {
				t.Errorf("error handler got %v, want %v", errs, want)
			}
		})
	})

	t.Run("handed over", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			b := newBroadcastor[int](t)
			handled, closed := &recorder[int]{}, &recorder[int]{}
			subscribe(t, b, func(ctx context.Context, id uuid.UUID, msg int) error {
				handled.record(msg)
				if err := b.Unsubscribe(ctx, id, subscriber.WithUnsubscribeDiscard()); err != nil {
					t.Errorf("Unsubscribe: %v", err)
				}

				return nil
			}, subscriber.WithErrorHandler[int](recordClosed(t, closed)))
			b.BroadcastBatch(t.Context(), batchOf(1, 2, 3))
			synctest.Wait()

			if got := handled.messages(); !slices.Equal(got, []int{1}) {
				t.Errorf("handle got %v, want [1]", got)
			}
			if got := closed.messages(); !slices.Equal(got, []int{2, 3}) {
				t.Errorf("error handler got *subscriber.ClosedError for %v, want [2 3]", got)
			}
		})
	})
}

// A SubscribeSeq loop gets a batch one value at a time. After a break, the values it did not get are reported one at a
// time.
func TestBroadcastBatch_SubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		closed := &recorder[int]{}
		_, seq := subscribeSeq(t, b, subscriber.WithBuffer[int](1), subscriber.WithErrorHandler[int](recordClosed(t, closed)))
		b.BroadcastBatch(t.Context(), batchOf(1, 2, 3))

		var got []int
		for msg := range seq {
			got = append(got, msg)
			if msg == 2 {
				break
			}
		}
		synctest.Wait()

		if !slices.Equal(got, []int{1, 2}) {
			t.Errorf("loop got %v, want [1 2]", got)
		}
		if got := closed.messages(); !slices.Equal(got, []int{3}) {
			t.Errorf("error handler got *subscriber.ClosedError for %v, want [3]", got)
		}
	})
}

// holder.NewBatch gathers what the subscriber takes, singles and batches' values alike, into batches of n, released
// before the next value is held, then what is left once its window ends, or at Shutdown. Stats count values held.
func TestSubscriberWithHolder_Batch(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[int](t)
		singles, batches := &recorder[int]{}, &batchRecorder{}
		id := subscribe(t, b, singles.handle, subscriber.WithHolder(holder.NewBatch[int](holdFor, 3)),
			subscriber.WithBatchHandler(batches.handle))

		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		b.BroadcastBatch(t.Context(), batchOf(3, 4, 5, 6, 7))
		synctest.Wait()
		checkBatches(t, batches, [][]int{{1, 2, 3}, {4, 5, 6}})
		checkStats(t, b, id, subscriber.Stats{Delivered: 7, Handled: 6, Held: 1, Batches: 2})

		time.Sleep(holdFor)
		synctest.Wait()
		checkBatches(t, batches, [][]int{{1, 2, 3}, {4, 5, 6}, {7}})
		checkStats(t, b, id, subscriber.Stats{Delivered: 7, Handled: 7, Batches: 3})

		b.Broadcast(t.Context(), 8)
		start := time.Now()
		if err := waitShutdown(t, shutdown(t.Context(), b)); err != nil {
			t.Errorf("Shutdown = %v, want nil", err)
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("Shutdown waited %v for the window to end, want it released at once", waited)
		}
		checkBatches(t, batches, [][]int{{1, 2, 3}, {4, 5, 6}, {7}, {8}})
		if got := singles.messages(); len(got) != 0 {
			t.Errorf("handle got %v, want nothing", got)
		}
	})
}

// However sent, each value of every batch a Broadcast picks a subscriber up for is counted once, a batch handler call
// counts in Batches, and the losses match the values the errors reported hold.
func TestStats_AddUpBatches(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		const broadcasters, perBroadcaster, size = 4, 50, 2
		b := newBroadcastor[int](t)
		reported := map[string]*atomic.Uint64{"unrolled": {}, "batch": {}, "seq": {}}
		options := func(name string) []subscriber.Option[int] {
			return []subscriber.Option[int]{
				subscriber.WithBuffer[int](1),
				subscriber.WithErrorHandler[int](func(_ context.Context, err error) { reported[name].Add(valuesLost(t, err)) }),
			}
		}
		ids := map[uuid.UUID]string{}
		ids[subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
			time.Sleep(time.Millisecond)
			if msg%3 == 0 {
				return handleError(msg)
			}

			return nil
		}, options("unrolled")...)] = "unrolled"
		ids[subscribe(t, b, (&recorder[int]{}).handle, append(options("batch"), subscriber.WithBatchHandler(
			func(_ context.Context, _ uuid.UUID, batch message.Batch[int]) error {
				time.Sleep(time.Millisecond)
				if first := slices.Collect(batch.All())[0]; first%3 == 0 {
					return batchError{len: batch.Len()}
				}

				return nil
			}))...)] = "batch"
		seqID, seq := subscribeSeq(t, b, options("seq")...)
		ids[seqID] = "seq"
		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			for msg, fail := range seq {
				time.Sleep(time.Millisecond)
				if msg%3 == 0 {
					fail(handleError(msg))
				}
			}
		}()

		deliveries := []message.Option[int]{
			message.WithSync[int](), message.WithParallel[int](), message.WithAsync[int](), message.WithNonBlocking[int](),
		}
		var wg sync.WaitGroup
		for g := range broadcasters {
			wg.Go(func() {
				for i := range perBroadcaster {
					first := (g*perBroadcaster + i) * size
					b.BroadcastBatch(t.Context(), batchOf(first, first+1), deliveries[i%len(deliveries)],
						message.WithTimeout[int](time.Millisecond))
				}
			})
		}
		waitGroup(t, &wg, "broadcasters")
		time.Sleep(time.Second) // until every async send and every handle is done
		synctest.Wait()

		stats := b.Stats()
		if len(stats) != len(ids) {
			t.Fatalf("Stats are for %d subscribers, want %d", len(stats), len(ids))
		}
		for _, s := range stats {
			name := ids[s.SubscriberID]
			if s.Queued != 0 || s.Sending != 0 || s.Handling != 0 || s.Delivered != s.Handled+s.Failed {
				t.Errorf("%s: %+v, want Delivered = Handled + Failed, and nothing queued, sending or handling", name, s)
			}
			if got := s.Handled + s.Failed + s.TimedOut + s.Dropped; got != broadcasters*perBroadcaster*size {
				t.Errorf("%s: %+v, counting %d values, want %d", name, s, got, broadcasters*perBroadcaster*size)
			}
			if lost, got := s.Failed+s.TimedOut+s.Dropped, reported[name].Load(); lost != got {
				t.Errorf("%s: %+v, counting %d values lost, want the %d the errors reported hold", name, s, lost, got)
			}
			calls, batches := s.Handled+s.Failed, uint64(0)
			if name == "batch" {
				calls, batches = (s.Handled+s.Failed)/size, (s.Handled+s.Failed)/size
			}
			if s.Batches != batches || s.HandleTime != time.Duration(calls)*time.Millisecond { //nolint:gosec // at most 400
				t.Errorf("%s: %+v, want %d batches and a HandleTime of %d calls", name, s, batches, calls)
			}
			if s.Handled == 0 || s.TimedOut+s.Dropped == 0 {
				t.Errorf("%s: %+v, want some values handled and some lost", name, s)
			}
		}

		if err := b.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		waitClosed(t, loopDone, "SubscribeSeq loop ended by Close")
		synctest.Wait()
	})
}

// batchOf returns a batch of values.
func batchOf(values ...int) *message.Slice[int] {
	batch := message.Slice[int](values)

	return &batch
}

// batchRecorder is a batch handler recording the values of every batch, in order.
type batchRecorder struct {
	mu      sync.Mutex
	batches [][]int
}

func (r *batchRecorder) handle(_ context.Context, _ uuid.UUID, batch message.Batch[int]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, slices.Collect(batch.All()))

	return nil
}

// checkBatches checks that r recorded want.
func checkBatches(t *testing.T, r *batchRecorder, want [][]int) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.EqualFunc(r.batches, want, slices.Equal[[]int]) {
		t.Errorf("batch handler got %v, want %v", r.batches, want)
	}
}

// recordMiddleware returns a middleware recording each message into r.
func recordMiddleware(r *recorder[int]) subscriber.Middleware[int] {
	return func(next subscriber.Handler[int]) subscriber.Handler[int] {
		return func(ctx context.Context, id uuid.UUID, msg int) error {
			r.record(msg)

			return next(ctx, id, msg)
		}
	}
}

// describeBatchLoss describes an error about a whole batch, such as "dropped [1 2]", and fails on any other error, or a
// *DroppedError or *ClosedError whose text does not name the batch.
func describeBatchLoss(t *testing.T, err error) string {
	t.Helper()
	var (
		dropped *subscriber.DroppedError[int]
		timeout *subscriber.TimeoutError[int]
		closed  *subscriber.ClosedError[int]
		evicted *subscriber.EvictedError[int]
		batch   message.Batch[int]
		what    string
	)
	switch {
	case errors.As(err, &evicted):
		batch, what = evicted.Batch, "evicted by "+evicted.Err.Error()
	case errors.As(err, &dropped):
		batch, what = dropped.Batch, "dropped"
	case errors.As(err, &timeout):
		batch, what = timeout.Batch, "timed out"
	case errors.As(err, &closed):
		batch, what = closed.Batch, "closed"
	default:
		t.Errorf("error handler got %v, want an error about a batch", err)

		return err.Error()
	}
	if batch == nil {
		t.Errorf("%v holds no batch", err)

		return what
	}
	if named := strings.Contains(err.Error(), fmt.Sprintf("batch of %d messages", batch.Len())); !named && (dropped != nil || closed != nil) {
		t.Errorf("%q does not name the batch", err)
	}

	return fmt.Sprintf("%s %v", what, slices.Collect(batch.All()))
}

// batchError is a batch handler's error, for a batch of len values.
type batchError struct {
	len int
}

func (e batchError) Error() string {
	return fmt.Sprintf("handling a batch of %d failed", e.len)
}

// valuesLost returns how many values err reports lost: a lost batch's, a batch handler's, or else one.
func valuesLost(t *testing.T, err error) uint64 {
	t.Helper()
	var (
		dropped *subscriber.DroppedError[int]
		timeout *subscriber.TimeoutError[int]
		failed  batchError
		batch   message.Batch[int]
	)
	switch {
	case errors.As(err, &failed):
		return uint64(failed.len) //nolint:gosec // a length
	case errors.As(err, &dropped):
		batch = dropped.Batch
	case errors.As(err, &timeout):
		batch = timeout.Batch
	case errors.As(err, new(handleError)):
		return 1
	default:
		t.Errorf("error handler got %v, want a loss or handle's error", err)

		return 0
	}
	if batch == nil {
		return 1
	}

	return uint64(batch.Len()) //nolint:gosec // a length
}

// tally is a batch of its own type.
type tally struct {
	values []int
}

func (b *tally) Add(msg int) {
	b.values = append(b.values, msg)
}

func (b *tally) All() iter.Seq[int] {
	return slices.Values(b.values)
}

func (b *tally) Len() int {
	return len(b.values)
}

func (b *tally) New() message.Batch[int] {
	return &tally{}
}
