package store_test

import (
	"cmp"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

var _ subscriber.History[int] = (*store.History[int])(nil)

// Read returns the last messages appended, by offset, and drops the first ones once full.
func TestHistory(t *testing.T) {
	t.Parallel()

	h := store.NewHistory[int](3)
	if got := read(t, h); len(got) != 0 {
		t.Errorf("Read of an empty history = %v, want nothing", got)
	}
	for _, offset := range []uint64{1, 2, 4, 3, 5} {
		h.Append(t.Context(), offset, int(offset)*10) //nolint:gosec // at most 5
	}
	want := []subscriber.HistoryEntry[int]{{Offset: 3, Message: 30}, {Offset: 4, Message: 40}, {Offset: 5, Message: 50}}
	if got := read(t, h); !slices.Equal(got, want) {
		t.Errorf("Read = %v, want %v", got, want)
	}
	if n := h.Len(); n != 3 {
		t.Errorf("Len = %d, want 3", n)
	}
}

// Appends may run at once, and Read returns what they appended, by offset.
func TestHistory_Concurrent(t *testing.T) {
	t.Parallel()

	const appenders, perAppender, size = 4, 1000, 64
	h := store.NewHistory[int](size)
	var (
		offsets atomic.Uint64
		wg      sync.WaitGroup
	)
	for range appenders {
		wg.Go(func() {
			for range perAppender {
				offset := offsets.Add(1)
				h.Append(t.Context(), offset, int(offset)) //nolint:gosec // at most 4000
			}
		})
	}
	for range 100 {
		entries := read(t, h)
		if !slices.IsSortedFunc(entries, func(a, b subscriber.HistoryEntry[int]) int { return cmp.Compare(a.Offset, b.Offset) }) {
			t.Fatalf("Read returned %v, out of order", entries)
		}
	}
	wg.Wait()
	if entries := read(t, h); len(entries) != size {
		t.Errorf("Read once done returned %d entries, want %d", len(entries), size)
	}
}

func read(t *testing.T, h *store.History[int]) []subscriber.HistoryEntry[int] {
	t.Helper()
	entries, err := h.Read(t.Context())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	return entries
}
