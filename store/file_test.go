package store_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

var _ store.Queue[int] = (*store.File[int])(nil)

var (
	errDecode = errors.New("decode failed")
	errSync   = errors.New("sync failed")
	errEmpty  = errors.New("")
)

// The sizes the tests expect: a segment's header, and a record of a one-digit message without error, from a
// subscriber ID below 128.
const (
	headerSize = 8
	recordSize = 12
)

// Next returns the oldest entry, with the record Put was given, and the same one until it is acked. Each entry has an
// ID of its own.
func TestFile(t *testing.T) {
	t.Parallel()

	f := openFile(t, t.TempDir())
	id := uint64(7)
	for msg := 1; msg <= 3; msg++ {
		put(t, f, subscriber.Record[int]{SubscriberID: id, Message: msg})
	}
	if n := f.Len(); n != 3 {
		t.Errorf("Len = %d, want 3", n)
	}

	first := next(t, f)
	if want := (subscriber.Record[int]{SubscriberID: id, Message: 1}); first.Record != want {
		t.Errorf("Next = %+v, want %+v", first.Record, want)
	}
	if again := next(t, f); again != first {
		t.Errorf("Next before Ack = %+v, want %+v again", again, first)
	}
	ack(t, f, first.ID)
	second := next(t, f)
	if second.Message != 2 || second.ID == first.ID {
		t.Errorf("Next after Ack = %+v, want message 2 with an ID other than %q", second, first.ID)
	}

	if got, want := readAll(t, f), []int{2, 3}; !slices.Equal(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
	if n, dropped := f.Len(), f.Dropped(); n != 0 || dropped != 0 {
		t.Errorf("Len = %d and Dropped = %d once everything is acked, want 0 and 0", n, dropped)
	}
}

// An error read back keeps its text, and matches the subscriber sentinels the original matched, but no other error.
func TestFile_Err(t *testing.T) {
	t.Parallel()

	evicted := &subscriber.EvictedError[int]{
		SubscriberID: 7, Err: &subscriber.TimeoutError[int]{SubscriberID: 7, Err: context.DeadlineExceeded},
	}
	errs := []error{nil, errLost, evicted, &subscriber.DroppedError[int]{SubscriberID: 7}, errEmpty}
	sentinels := []error{
		subscriber.ErrClosed, subscriber.ErrTimeout, subscriber.ErrDropped, subscriber.ErrPanic, subscriber.ErrStore,
		subscriber.ErrEvicted, subscriber.ErrExpired,
	}
	dir := t.TempDir()
	f := openFile(t, dir)
	for msg, err := range errs {
		put(t, f, subscriber.Record[int]{Message: msg, Err: err})
	}
	closeFile(t, f)

	f = openFile(t, dir)
	for _, want := range errs {
		entry := next(t, f)
		ack(t, f, entry.ID)
		got := entry.Err
		if (got == nil) != (want == nil) {
			t.Errorf("message %d: read back error %v, want %v", entry.Message, got, want)

			continue
		}
		if want == nil {
			continue
		}
		if got.Error() != want.Error() {
			t.Errorf("message %d: read back error %q, want %q", entry.Message, got, want)
		}
		for _, sentinel := range sentinels {
			if errors.Is(got, sentinel) != errors.Is(want, sentinel) {
				t.Errorf("message %d: errors.Is(%v, %v) = %t, want %t",
					entry.Message, got, sentinel, errors.Is(got, sentinel), errors.Is(want, sentinel))
			}
		}
		if errors.Is(got, errLost) || errors.Is(got, context.DeadlineExceeded) {
			t.Errorf("message %d: read back error %v matches an error other than a subscriber sentinel", entry.Message, got)
		}
	}
}

// The entries not acked are back after a reopen, in order, with IDs no entry had before, and Put appends after them.
func TestFile_Reopen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := openFile(t, dir)
	putMessages(t, f, 1, 2, 3, 4, 5)
	ids := map[string]bool{}
	for range 2 {
		entry := next(t, f)
		ids[entry.ID] = true
		ack(t, f, entry.ID)
	}
	closeFile(t, f)

	f = openFile(t, dir)
	if n := f.Len(); n != 3 {
		t.Errorf("Len after reopening = %d, want 3", n)
	}
	putMessages(t, f, 6)
	closeFile(t, f)

	f = openFile(t, dir)
	var got []int
	for f.Len() > 0 {
		entry := next(t, f)
		if ids[entry.ID] {
			t.Errorf("message %d has ID %q, which an entry had before", entry.Message, entry.ID)
		}
		ids[entry.ID] = true
		got = append(got, entry.Message)
		ack(t, f, entry.ID)
	}
	if want := []int{3, 4, 5, 6}; !slices.Equal(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// Whatever length a power cut leaves a segment, OpenFile keeps exactly its whole records, and Put appends after them.
func TestFile_Truncated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := openFile(t, dir)
	all := []int{1, 2, 3, 4}
	putMessages(t, f, all...)
	closeFile(t, f)
	names := segmentNames(t, dir)
	if len(names) != 1 {
		t.Fatalf("segments %v, want 1", names)
	}
	data := readFile(t, filepath.Join(dir, names[0]))

	for size := range len(data) + 1 {
		cut := t.TempDir()
		writeFile(t, filepath.Join(cut, names[0]), data[:size])
		f := openFile(t, cut, store.WithSyncEvery(math.MaxInt))
		want := slices.Clone(all[:max(size-headerSize, 0)/recordSize])
		if n := f.Len(); n != len(want) {
			t.Errorf("cut to %d bytes: Len = %d, want %d", size, n, len(want))
		}
		putMessages(t, f, 9)
		closeFile(t, f)

		f = openFile(t, cut, store.WithSyncEvery(math.MaxInt))
		if got, want := readAll(t, f), append(want, 9); !slices.Equal(got, want) {
			t.Errorf("cut to %d bytes: read back %v, want %v", size, got, want)
		}
		closeFile(t, f)
	}
}

// A corrupted record loses its segment's records from it on, and no other.
func TestFile_Corrupted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := openFile(t, dir, store.WithSegmentSize(2*recordSize))
	putMessages(t, f, 1, 2, 3, 4)
	closeFile(t, f)
	names := segmentNames(t, dir)
	if len(names) != 2 {
		t.Fatalf("segments %v, want 2", names)
	}
	path := filepath.Join(dir, names[0])
	data := readFile(t, path)
	data[headerSize+recordSize+recordSize-1] ^= 0xff // message 2
	writeFile(t, path, data)

	f = openFile(t, dir, store.WithSegmentSize(2*recordSize))
	if got, want := readAll(t, f), []int{1, 3, 4}; !slices.Equal(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// The ack file has two slots: with the newest corrupted, OpenFile falls back to the other, and with neither, it hands
// every entry again.
func TestFile_AckFile(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		corrupt func(t *testing.T, path string)
		want    []int
	}{
		{"newest slot corrupted", func(t *testing.T, path string) {
			t.Helper()
			flipBytes(t, path, 12)
		}, []int{2, 3, 4}},
		{"both slots corrupted", func(t *testing.T, path string) {
			t.Helper()
			flipBytes(t, path, 0, 12)
		}, []int{1, 2, 3, 4}},
		{"missing", func(t *testing.T, path string) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, []int{1, 2, 3, 4}},
	} {
		dir := t.TempDir()
		f := openFile(t, dir)
		putMessages(t, f, 1, 2, 3, 4)
		for range 2 {
			ack(t, f, next(t, f).ID)
		}
		closeFile(t, f)
		tc.corrupt(t, filepath.Join(dir, "ack"))

		f = openFile(t, dir)
		if got := readAll(t, f); !slices.Equal(got, tc.want) {
			t.Errorf("%s: read back %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Put starts a segment once the newest is full, and a segment is removed once its entries are acked, but the newest.
func TestFile_Segments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := openFile(t, dir, store.WithSegmentSize(2*recordSize))
	putMessages(t, f, 1, 2, 3, 4, 5)
	if names := segmentNames(t, dir); len(names) != 3 {
		t.Errorf("segments %v after 5 Puts, want 3", names)
	}
	for msg, want := range []int{3, 2, 2, 1, 1} {
		ack(t, f, next(t, f).ID)
		if names := segmentNames(t, dir); len(names) != want {
			t.Errorf("segments %v after acking message %d, want %d", names, msg+1, want)
		}
	}

	// The newest segment is full and acked when Put starts another.
	putMessages(t, f, 6)
	ack(t, f, next(t, f).ID)
	putMessages(t, f, 7)
	if names := segmentNames(t, dir); len(names) != 1 {
		t.Errorf("segments %v once Put starts one after an acked one, want 1", names)
	}
	if got, want := readAll(t, f), []int{7}; !slices.Equal(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// Once WithMaxSize is reached, Put drops the oldest segment, even an entry Next returned, whose Ack is then ignored,
// and counts its entries.
func TestFile_MaxSize(t *testing.T) {
	t.Parallel()

	maxSize := int64(2 * (headerSize + 3*recordSize))
	dir := t.TempDir()
	f := openFile(t, dir, store.WithSegmentSize(3*recordSize), store.WithMaxSize(maxSize))
	putMessages(t, f, 1, 2, 3, 4, 5, 6)
	handled := next(t, f)
	putMessages(t, f, 7)
	if n, dropped := f.Len(), f.Dropped(); n != 4 || dropped != 3 {
		t.Errorf("Len = %d and Dropped = %d, want 4 and 3", n, dropped)
	}
	ack(t, f, handled.ID)
	if n := f.Len(); n != 4 {
		t.Errorf("Len after acking a dropped entry = %d, want 4", n)
	}
	if size := dirSize(t, dir); size > maxSize {
		t.Errorf("segments take %d bytes, want at most %d", size, maxSize)
	}
	if got, want := readAll(t, f), []int{4, 5, 6, 7}; !slices.Equal(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// With only the newest segment left, Put starts another, so as to drop it.
func TestFile_MaxSizeOneSegment(t *testing.T) {
	t.Parallel()

	maxSize := int64(headerSize + 2*recordSize)
	dir := t.TempDir()
	f := openFile(t, dir, store.WithMaxSize(maxSize))
	putMessages(t, f, 1, 2, 3)
	if n, dropped := f.Len(), f.Dropped(); n != 1 || dropped != 2 {
		t.Errorf("Len = %d and Dropped = %d, want 1 and 2", n, dropped)
	}
	if size := dirSize(t, dir); size > maxSize {
		t.Errorf("segments take %d bytes, want at most %d", size, maxSize)
	}
	if got, want := readAll(t, f), []int{3}; !slices.Equal(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// A record larger than WithMaxSize gets ErrTooLarge, and is not stored.
func TestFile_TooLarge(t *testing.T) {
	t.Parallel()

	f := openFile(t, t.TempDir(), store.WithMaxSize(headerSize+recordSize))
	putMessages(t, f, 1)
	if err := f.Put(t.Context(), subscriber.Record[int]{Message: 10}); !errors.Is(err, store.ErrTooLarge) {
		t.Errorf("Put of a record larger than the cap = %v, want %v", err, store.ErrTooLarge)
	}
	if got, want := readAll(t, f), []int{1}; !slices.Equal(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// A File syncs after each write by default, every n writes with WithSyncEvery, and d after a write with
// WithSyncInterval, never while idle. Close syncs what is left.
func TestFile_Sync(t *testing.T) {
	t.Parallel()

	t.Run("default", func(t *testing.T) {
		t.Parallel()

		f := openFile(t, t.TempDir())
		s := countSyncs(f)
		putMessages(t, f, 1, 2)
		s.check(t, "2 Puts", map[string]int{"segment": 2, "dir": 1})
		ack(t, f, next(t, f).ID)
		s.check(t, "an Ack", map[string]int{"segment": 2, "dir": 1, "ack": 1})
		closeFile(t, f)
		s.check(t, "Close", map[string]int{"segment": 2, "dir": 1, "ack": 1})
	})

	t.Run("every", func(t *testing.T) {
		t.Parallel()

		f := openFile(t, t.TempDir(), store.WithSyncEvery(3))
		s := countSyncs(f)
		putMessages(t, f, 1, 2)
		s.check(t, "2 Puts", map[string]int{})
		putMessages(t, f, 3)
		s.check(t, "3 Puts", map[string]int{"segment": 1, "dir": 1})
		ack(t, f, next(t, f).ID)
		closeFile(t, f)
		s.check(t, "Close", map[string]int{"segment": 1, "dir": 1, "ack": 1})
	})

	t.Run("interval", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			f := openFile(t, t.TempDir(), store.WithSyncInterval(time.Second))
			s := countSyncs(f)
			putMessages(t, f, 1, 2)
			synctest.Wait()
			s.check(t, "2 Puts", map[string]int{})
			time.Sleep(time.Second)
			synctest.Wait()
			s.check(t, "a second", map[string]int{"segment": 1, "dir": 1})
			time.Sleep(10 * time.Second)
			synctest.Wait()
			s.check(t, "idle", map[string]int{"segment": 1, "dir": 1})
			putMessages(t, f, 3)
			closeFile(t, f)
			s.check(t, "Close", map[string]int{"segment": 2, "dir": 1})
		})
	})
}

// Once a sync fails, every write fails with its error, and Close too, but Next still reads.
func TestFile_SyncFails(t *testing.T) {
	t.Parallel()

	f := openFile(t, t.TempDir())
	store.SetFsync(f, func(*os.File) error { return errSync })
	for msg := 1; msg <= 2; msg++ {
		if err := f.Put(t.Context(), subscriber.Record[int]{Message: msg}); !errors.Is(err, errSync) {
			t.Errorf("Put %d = %v, want %v", msg, err, errSync)
		}
	}
	entry := next(t, f)
	if entry.Message != 1 {
		t.Errorf("Next = %+v, want message 1", entry)
	}
	if err := f.Ack(t.Context(), entry.ID); !errors.Is(err, errSync) {
		t.Errorf("Ack = %v, want %v", err, errSync)
	}
	if err := f.Close(); !errors.Is(err, errSync) {
		t.Errorf("Close = %v, want %v", err, errSync)
	}
}

// Next waits until there is an entry, and wakes up as soon as Put adds one.
func TestFile_NextWaits(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f := openFile(t, t.TempDir())
		for msg := 1; msg <= 2; msg++ {
			got := goNext(t.Context(), f)
			synctest.Wait()
			select {
			case res := <-got:
				t.Fatalf("Next on an empty File returned %+v, %v, want it to wait", res.entry, res.err)
			default:
			}

			putMessages(t, f, msg)
			res := receive(t, got)
			if res.err != nil || res.entry.Message != msg {
				t.Fatalf("Next = %+v, %v, want message %d", res.entry, res.err, msg)
			}
			ack(t, f, res.entry.ID)
		}
	})
}

// Once ctx is done, Next returns its error, whether it was waiting or there is an entry.
func TestFile_NextContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f := openFile(t, t.TempDir())
		ctx, cancel := context.WithCancel(t.Context())
		got := goNext(ctx, f)
		synctest.Wait()
		cancel()
		if res := receive(t, got); !errors.Is(res.err, context.Canceled) {
			t.Errorf("Next once ctx is done while waiting = %+v, %v, want %v", res.entry, res.err, context.Canceled)
		}

		putMessages(t, f, 1)
		if entry, err := f.Next(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("Next with a done ctx and an entry = %+v, %v, want %v", entry, err, context.Canceled)
		}
	})
}

// Close wakes up a Next waiting, and every call after it returns os.ErrClosed.
func TestFile_Close(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f := openFile(t, t.TempDir())
		got := goNext(t.Context(), f)
		synctest.Wait()
		closeFile(t, f)
		if res := receive(t, got); !errors.Is(res.err, os.ErrClosed) {
			t.Errorf("Next waiting once closed = %+v, %v, want %v", res.entry, res.err, os.ErrClosed)
		}
		if err := f.Put(t.Context(), subscriber.Record[int]{Message: 1}); !errors.Is(err, os.ErrClosed) {
			t.Errorf("Put once closed = %v, want %v", err, os.ErrClosed)
		}
		if err := f.Ack(t.Context(), "0"); !errors.Is(err, os.ErrClosed) {
			t.Errorf("Ack once closed = %v, want %v", err, os.ErrClosed)
		}
		if err := f.Close(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("Close once closed = %v, want %v", err, os.ErrClosed)
		}
	})
}

// Concurrent Puts while a reader reads and acks: the reader gets every entry once, each goroutine's in order.
func TestFile_Concurrent(t *testing.T) {
	t.Parallel()

	const writers, perWriter = 8, 100
	f := openFile(t, t.TempDir(), store.WithSegmentSize(64*recordSize), store.WithSyncEvery(64))
	ctx, cancel := context.WithTimeout(t.Context(), deadlockTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range perWriter {
				if err := f.Put(ctx, subscriber.Record[int]{Message: w*perWriter + i}); err != nil {
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
		entry, err := f.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if w := entry.Message / perWriter; entry.Message <= last[w] {
			t.Errorf("writer %d: read %d after %d", w, entry.Message, last[w])
		} else {
			last[w] = entry.Message
		}
		ack(t, f, entry.ID)
	}
	wg.Wait()

	if n, dropped := f.Len(), f.Dropped(); n != 0 || dropped != 0 {
		t.Errorf("Len = %d and Dropped = %d once everything is read back, want 0 and 0", n, dropped)
	}
}

// Replay yields what the File held when ranged, and acks each value once yield returns: a value put meanwhile, as one
// lost again, is not yielded, and one whose yield returned false stays.
func TestFile_Replay(t *testing.T) {
	t.Parallel()

	f := openFile(t, t.TempDir())
	putMessages(t, f, 1, 2, 3)
	var got []int
	for msg := range f.Replay(t.Context()) {
		got = append(got, msg)
		if msg == 1 {
			putMessages(t, f, 1)
		}
	}
	if want := []int{1, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("Replay yielded %v, want %v", got, want)
	}

	putMessages(t, f, 2, 3)
	got = nil
	for msg := range f.Replay(t.Context()) {
		got = append(got, msg)
		if msg == 2 {
			break
		}
	}
	if want := []int{1, 2}; !slices.Equal(got, want) {
		t.Errorf("Replay with a break yielded %v, want %v", got, want)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for msg := range f.Replay(ctx) {
		t.Errorf("Replay with a done ctx yielded %d", msg)
	}
	if got, want := readAll(t, f), []int{2, 3}; !slices.Equal(got, want) {
		t.Errorf("left %v, want %v", got, want)
	}
}

// An entry decode rejects is a *DecodeError with its ID, which stays until acked, and Replay stops at it.
func TestFile_DecodeError(t *testing.T) {
	t.Parallel()

	f, err := store.OpenFile(t.TempDir(), encodeInt, func(b []byte) (int, error) {
		if string(b) == "2" {
			return 0, errDecode
		}

		return decodeInt(b)
	})
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	putMessages(t, f, 1, 2, 3)
	var got []int
	for msg := range f.Replay(t.Context()) {
		got = append(got, msg)
	}
	if want := []int{1}; !slices.Equal(got, want) {
		t.Errorf("Replay yielded %v, want %v", got, want)
	}

	var decodeErr *store.DecodeError
	for range 2 {
		entry, err := f.Next(t.Context())
		if !errors.As(err, &decodeErr) || !errors.Is(err, errDecode) {
			t.Fatalf("Next = %+v, %v, want a *DecodeError wrapping %v", entry, err, errDecode)
		}
	}
	ack(t, f, decodeErr.ID)
	if got, want := readAll(t, f), []int{3}; !slices.Equal(got, want) {
		t.Errorf("read back %v after acking the entry, want %v", got, want)
	}
}

// Whatever a segment and the ack file hold, OpenFile reads back what it finds valid, the same after a reopen, and Put
// appends after it.
func FuzzOpenFile(f *testing.F) {
	dir := f.TempDir()
	q, err := store.OpenFile(dir, encodeString, decodeString)
	if err != nil {
		f.Fatal(err)
	}
	ctx := f.Context()
	for _, r := range []subscriber.Record[string]{{Message: "a"}, {SubscriberID: 300, Message: "bb", Err: errLost}, {Message: "ccc"}} {
		if err := q.Put(ctx, r); err != nil {
			f.Fatal(err)
		}
	}
	entry, err := q.Next(ctx)
	if err != nil {
		f.Fatal(err)
	}
	if err := q.Ack(ctx, entry.ID); err != nil {
		f.Fatal(err)
	}
	if err := q.Close(); err != nil {
		f.Fatal(err)
	}
	segment := readFile(f, filepath.Join(dir, firstSegment))
	ack := readFile(f, filepath.Join(dir, "ack"))
	f.Add(segment, ack)
	f.Add(segment[:len(segment)-3], ack)
	f.Add(segment, ack[:5])
	flipped := slices.Clone(segment)
	flipped[headerSize+recordSize+5] ^= 1
	f.Add(flipped, ack)
	f.Add([]byte{}, []byte{})

	f.Fuzz(func(t *testing.T, segment, ack []byte) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, firstSegment), segment)
		writeFile(t, filepath.Join(dir, "ack"), ack)

		q := openStrings(t, dir)
		n := q.Len()
		closeFile(t, q)
		q = openStrings(t, dir)
		if got := readStrings(t, q); len(got) != n {
			t.Errorf("read back %d entries after a reopen, want Len %d", len(got), n)
		}
		if err := q.Put(t.Context(), subscriber.Record[string]{Message: "new"}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		closeFile(t, q)
		q = openStrings(t, dir)
		if got, want := readStrings(t, q), []string{"new"}; !slices.Equal(got, want) {
			t.Errorf("read back %q after acking everything and a Put, want %q", got, want)
		}
		closeFile(t, q)
	})
}

func BenchmarkFile_PutAck(b *testing.B) {
	for _, bc := range []struct {
		name    string
		options []store.FileOption
	}{
		{"sync=every", nil},
		{"sync=100", []store.FileOption{store.WithSyncEvery(100)}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			f, err := store.OpenFile(b.TempDir(), encodeInt, decodeInt, bc.options...)
			if err != nil {
				b.Fatal(err)
			}
			ctx := b.Context()
			for b.Loop() {
				if err := f.Put(ctx, subscriber.Record[int]{Message: 1}); err != nil {
					b.Fatal(err)
				}
				entry, err := f.Next(ctx)
				if err != nil {
					b.Fatal(err)
				}
				if err := f.Ack(ctx, entry.ID); err != nil {
					b.Fatal(err)
				}
			}
			if err := f.Close(); err != nil {
				b.Fatal(err)
			}
		})
	}
}

// firstSegment is the name of a File's first segment.
const firstSegment = "00000000000000000000.seg"

func encodeInt(msg int) ([]byte, error) {
	return strconv.AppendInt(nil, int64(msg), 10), nil
}

func decodeInt(b []byte) (int, error) {
	return strconv.Atoi(string(b))
}

func encodeString(msg string) ([]byte, error) {
	return []byte(msg), nil
}

func decodeString(b []byte) (string, error) {
	return string(b), nil
}

// openFile opens the File of ints in dir, and closes it once the test is done, unless the test did.
func openFile(t *testing.T, dir string, options ...store.FileOption) *store.File[int] {
	t.Helper()
	f, err := store.OpenFile(dir, encodeInt, decodeInt, options...)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	return f
}

// openStrings opens the File of strings in dir, which never fails to decode, and closes it once the test is done,
// unless the test did.
func openStrings(t *testing.T, dir string) *store.File[string] {
	t.Helper()
	f, err := store.OpenFile(dir, encodeString, decodeString)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	return f
}

func closeFile[T any](t *testing.T, f *store.File[T]) {
	t.Helper()
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// readStrings reads back and acks every entry f holds, and returns their messages, oldest first.
func readStrings(t *testing.T, f *store.File[string]) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), deadlockTimeout)
	defer cancel()
	var msgs []string
	for f.Len() > 0 {
		entry, err := f.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		msgs = append(msgs, entry.Message)
		if err := f.Ack(ctx, entry.ID); err != nil {
			t.Fatalf("Ack: %v", err)
		}
	}

	return msgs
}

// syncs counts the files a File syncs: its segments, its directory and its ack file.
type syncs struct {
	mu     sync.Mutex
	counts map[string]int
}

func countSyncs[T any](f *store.File[T]) *syncs {
	s := &syncs{counts: map[string]int{}}
	store.SetFsync(f, func(file *os.File) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch name := filepath.Base(file.Name()); {
		case strings.HasSuffix(name, ".seg"):
			s.counts["segment"]++
		case name == "ack":
			s.counts["ack"]++
		default:
			s.counts["dir"]++
		}

		return file.Sync()
	})

	return s
}

func (s *syncs) check(t *testing.T, after string, want map[string]int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, kind := range []string{"segment", "dir", "ack"} {
		if s.counts[kind] != want[kind] {
			t.Errorf("after %s: %d syncs of the %s, want %d", after, s.counts[kind], kind, want[kind])
		}
	}
}

// segmentNames returns the names of the segments in dir, in order.
func segmentNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".seg") {
			names = append(names, entry.Name())
		}
	}

	return names
}

// dirSize returns the bytes the segments in dir take.
func dirSize(t *testing.T, dir string) int64 {
	t.Helper()
	var size int64
	for _, name := range segmentNames(t, dir) {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		size += info.Size()
	}

	return size
}

func readFile(tb testing.TB, path string) []byte {
	tb.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // the test's own file
	if err != nil {
		tb.Fatal(err)
	}

	return data
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// flipBytes flips the bytes at the given offsets of the file at path.
func flipBytes(t *testing.T, path string, offsets ...int) {
	t.Helper()
	data := readFile(t, path)
	for _, offset := range offsets {
		data[offset] ^= 0xff
	}
	writeFile(t, path, data)
}
