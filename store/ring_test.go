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

	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

var _ store.Queue[int] = (*store.Ring[int])(nil)

var errLost = errors.New("lost")

// deadlockTimeout bounds every wait, so a deadlock fails the test instead of hanging.
const deadlockTimeout = 10 * time.Second

// Next returns the oldest entry, with the record Put was given, and the same one until it is acked. Each entry has an
// ID of its own.
func TestRing(t *testing.T) {
	t.Parallel()

	r := store.NewRing[int](4)
	id := uuid.New()
	for msg := 1; msg <= 3; msg++ {
		put(t, r, subscriber.Record[int]{SubscriberID: id, Message: msg, Err: errLost})
	}
	if n := r.Len(); n != 3 {
		t.Errorf("Len = %d, want 3", n)
	}

	first := next(t, r)
	if want := (subscriber.Record[int]{SubscriberID: id, Message: 1, Err: errLost}); first.Record != want {
		t.Errorf("Next = %+v, want %+v", first.Record, want)
	}
	if again := next(t, r); again != first {
		t.Errorf("Next before Ack = %+v, want %+v again", again, first)
	}
	ack(t, r, first.ID)
	second := next(t, r)
	if second.Message != 2 || second.ID == first.ID {
		t.Errorf("Next after Ack = %+v, want message 2 with an ID other than %q", second, first.ID)
	}

	if got, want := readAll(t, r), []int{2, 3}; !slices.Equal(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
	if n, dropped := r.Len(), r.Dropped(); n != 0 || dropped != 0 {
		t.Errorf("Len = %d and Dropped = %d once everything is acked, want 0 and 0", n, dropped)
	}
}

// Once the ring is full, Put drops the oldest entry and counts it, and the ring keeps its order as it wraps around.
func TestRing_Full(t *testing.T) {
	t.Parallel()

	r := store.NewRing[int](3)
	putMessages(t, r, 1, 2, 3, 4, 5)
	if n, dropped := r.Len(), r.Dropped(); n != 3 || dropped != 2 {
		t.Errorf("Len = %d and Dropped = %d, want 3 and 2", n, dropped)
	}
	if got, want := readAll(t, r), []int{3, 4, 5}; !slices.Equal(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}

	putMessages(t, r, 6, 7)
	if got, want := readAll(t, r), []int{6, 7}; !slices.Equal(got, want) {
		t.Errorf("read back %v after wrapping around, want %v", got, want)
	}
	if dropped := r.Dropped(); dropped != 2 {
		t.Errorf("Dropped = %d, want 2", dropped)
	}
}

// NewRing makes room for at least one entry.
func TestNewRing_Size(t *testing.T) {
	t.Parallel()

	for _, size := range []int{-1, 0, 1} {
		r := store.NewRing[int](size)
		putMessages(t, r, 1, 2)
		if n, dropped := r.Len(), r.Dropped(); n != 1 || dropped != 1 {
			t.Errorf("NewRing(%d): Len = %d and Dropped = %d, want 1 and 1", size, n, dropped)
		}
		if got, want := readAll(t, r), []int{2}; !slices.Equal(got, want) {
			t.Errorf("NewRing(%d): read back %v, want %v", size, got, want)
		}
	}
}

// Ack ignores an ID the ring does not hold: never given, already acked, or dropped while handled, after which Next goes
// on with the next entry.
func TestRing_AckIgnored(t *testing.T) {
	t.Parallel()

	r := store.NewRing[int](2)
	putMessages(t, r, 1, 2)
	ack(t, r, "unknown")
	if n := r.Len(); n != 2 {
		t.Errorf("Len after acking an unknown ID = %d, want 2", n)
	}

	first := next(t, r)
	ack(t, r, first.ID)
	ack(t, r, first.ID)
	if n := r.Len(); n != 1 {
		t.Errorf("Len after acking the same ID twice = %d, want 1", n)
	}

	second := next(t, r)
	putMessages(t, r, 3, 4) // drops 2, which is being handled
	ack(t, r, second.ID)
	if n, dropped := r.Len(), r.Dropped(); n != 2 || dropped != 1 {
		t.Errorf("Len = %d and Dropped = %d after acking a dropped entry, want 2 and 1", n, dropped)
	}
	if got, want := readAll(t, r), []int{3, 4}; !slices.Equal(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// Next waits until there is an entry, and wakes up as soon as Put adds one. A Next that spun would hang this test.
func TestRing_NextWaits(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		r := store.NewRing[int](2)
		for msg := 1; msg <= 2; msg++ {
			got := goNext(t.Context(), r)
			synctest.Wait()
			select {
			case res := <-got:
				t.Fatalf("Next on an empty ring returned %+v, %v, want it to wait", res.entry, res.err)
			default:
			}

			putMessages(t, r, msg)
			res := receive(t, got)
			if res.err != nil || res.entry.Message != msg {
				t.Fatalf("Next = %+v, %v, want message %d", res.entry, res.err, msg)
			}
			ack(t, r, res.entry.ID)
		}
	})
}

// Once ctx is done, Next returns its error, whether it was waiting or there is an entry.
func TestRing_NextContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		r := store.NewRing[int](2)
		ctx, cancel := context.WithCancel(t.Context())
		got := goNext(ctx, r)
		synctest.Wait()
		cancel()
		if res := receive(t, got); !errors.Is(res.err, context.Canceled) {
			t.Errorf("Next once ctx is done while waiting = %+v, %v, want %v", res.entry, res.err, context.Canceled)
		}

		putMessages(t, r, 1)
		if entry, err := r.Next(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("Next with a done ctx and an entry = %+v, %v, want %v", entry, err, context.Canceled)
		}
		if n := r.Len(); n != 1 {
			t.Errorf("Len = %d, want 1: Next with a done ctx takes nothing", n)
		}
	})
}

// Concurrent Puts while a reader reads and acks: the reader gets every entry once, each goroutine's in order.
func TestRing_Concurrent(t *testing.T) {
	t.Parallel()

	const writers, perWriter = 8, 100
	r := store.NewRing[int](writers * perWriter)
	ctx, cancel := context.WithTimeout(t.Context(), deadlockTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range perWriter {
				if err := r.Put(ctx, subscriber.Record[int]{Message: w*perWriter + i}); err != nil {
					t.Errorf("Put: %v", err)
				}
			}
		})
	}
	last := make([]int, writers)
	for i := range last {
		last[i] = -1
	}
	for range writers * perWriter {
		entry, err := r.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if w := entry.Message / perWriter; entry.Message <= last[w] {
			t.Errorf("writer %d: read %d after %d", w, entry.Message, last[w])
		} else {
			last[w] = entry.Message
		}
		ack(t, r, entry.ID)
	}
	wg.Wait()

	if n, dropped := r.Len(), r.Dropped(); n != 0 || dropped != 0 {
		t.Errorf("Len = %d and Dropped = %d once everything is read back, want 0 and 0", n, dropped)
	}
}

type nextResult struct {
	entry store.Entry[int]
	err   error
}

// goNext calls r.Next from a goroutine of its own, and returns a channel that yields what it returns.
func goNext(ctx context.Context, r *store.Ring[int]) <-chan nextResult {
	got := make(chan nextResult, 1)
	go func() {
		entry, err := r.Next(ctx)
		got <- nextResult{entry, err}
	}()

	return got
}

func receive(t *testing.T, got <-chan nextResult) nextResult {
	t.Helper()
	select {
	case res := <-got:
		return res
	case <-time.After(deadlockTimeout):
		t.Fatalf("still waiting for Next after %v: deadlock", deadlockTimeout)

		return nextResult{}
	}
}

func put(t *testing.T, r *store.Ring[int], record subscriber.Record[int]) {
	t.Helper()
	if err := r.Put(t.Context(), record); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func putMessages(t *testing.T, r *store.Ring[int], msgs ...int) {
	t.Helper()
	for _, msg := range msgs {
		put(t, r, subscriber.Record[int]{Message: msg})
	}
}

// next returns what r.Next returns, which must not wait.
func next(t *testing.T, r *store.Ring[int]) store.Entry[int] {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), deadlockTimeout)
	defer cancel()
	entry, err := r.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}

	return entry
}

func ack(t *testing.T, r *store.Ring[int], id string) {
	t.Helper()
	if err := r.Ack(t.Context(), id); err != nil {
		t.Fatalf("Ack(%q): %v", id, err)
	}
}

// readAll reads back and acks every entry r holds, and returns their messages, oldest first.
func readAll(t *testing.T, r *store.Ring[int]) []int {
	t.Helper()
	var msgs []int
	for r.Len() > 0 {
		entry := next(t, r)
		msgs = append(msgs, entry.Message)
		ack(t, r, entry.ID)
	}

	return msgs
}
