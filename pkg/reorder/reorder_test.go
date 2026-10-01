package reorder_test

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/elojah/broadcastor/pkg/reorder"
)

// t0 is when every test starts. Time is only ever passed in, so the tests need no clock.
var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Each value is held until its window ends, then released in order.
func TestBuffer_Window(t *testing.T) {
	t.Parallel()

	b := reorder.New(cmp.Compare[int], time.Second, 0)
	push(t, b, t0, 3, 1, 2)
	if got := popAll(b, t0.Add(time.Second-1)); len(got) != 0 {
		t.Errorf("before the windows ended, Pop released %v, want nothing", got)
	}
	if got, want := popAll(b, t0.Add(time.Second)), []int{1, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("once the windows ended, Pop released %v, want %v", got, want)
	}
}

// With a window of 0, Pop releases right away, in order, whatever was pushed.
func TestBuffer_NoWindow(t *testing.T) {
	t.Parallel()

	b := reorder.New(cmp.Compare[int], 0, 0)
	push(t, b, t0, 3, 1, 2)
	if got, want := popAll(b, t0), []int{1, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("Pop released %v, want %v", got, want)
	}
}

// When a value's window ends, every value held that sorts before it is released first, however recently pushed.
func TestBuffer_DueWithEarlier(t *testing.T) {
	t.Parallel()

	b := reorder.New(cmp.Compare[int], time.Second, 0)
	push(t, b, t0, 5)
	push(t, b, t0.Add(time.Second/2), 7, 1)
	if got, want := popAll(b, t0.Add(time.Second)), []int{1, 5}; !slices.Equal(got, want) {
		t.Errorf("once 5's window ended, Pop released %v, want %v", got, want)
	}
	if got, want := popAll(b, t0.Add(3*time.Second/2)), []int{7}; !slices.Equal(got, want) {
		t.Errorf("once 7's window ended, Pop released %v, want %v", got, want)
	}
}

// Past the limit, the first value in order is released before its window ends.
func TestBuffer_Limit(t *testing.T) {
	t.Parallel()

	b := reorder.New(cmp.Compare[int], time.Hour, 2)
	push(t, b, t0, 3, 1, 2)
	if got, want := popAll(b, t0), []int{1}; !slices.Equal(got, want) {
		t.Errorf("Pop released %v, want %v", got, want)
	}
	if got := b.Len(); got != 2 {
		t.Errorf("Len = %d, want 2", got)
	}
}

// A value that sorts before one already released is late, and is not held. An equal one is not late.
func TestBuffer_Late(t *testing.T) {
	t.Parallel()

	b := reorder.New(cmp.Compare[int], 0, 0)
	if _, ok := b.Last(); ok {
		t.Error("Last reported a value before any was released")
	}
	push(t, b, t0, 2)
	popAll(b, t0)
	if b.Push(1, t0) {
		t.Error("Push(1) after 2 was released held it, want it late")
	}
	if last, ok := b.Last(); !ok || last != 2 {
		t.Errorf("Last = %d, %t, want 2, true", last, ok)
	}
	if !b.Push(2, t0) {
		t.Error("Push(2) after 2 was released reported it late, want it held")
	}
}

// Equal values keep the order they were pushed in.
func TestBuffer_Ties(t *testing.T) {
	t.Parallel()

	type value struct{ key, id int }
	b := reorder.New(func(x, y value) int { return cmp.Compare(x.key, y.key) }, 0, 0)
	for _, v := range []value{{2, 1}, {1, 2}, {2, 3}, {1, 4}, {2, 5}} {
		if !b.Push(v, t0) {
			t.Fatalf("Push(%v) reported it late", v)
		}
	}
	if got, want := popAll(b, t0), []value{{1, 2}, {1, 4}, {2, 1}, {2, 3}, {2, 5}}; !slices.Equal(got, want) {
		t.Errorf("Pop released %v, want %v", got, want)
	}
}

// Release releases in order whatever is held, due or not.
func TestBuffer_Release(t *testing.T) {
	t.Parallel()

	b := reorder.New(cmp.Compare[int], time.Hour, 0)
	push(t, b, t0, 3, 1, 2)
	var got []int
	for v, ok := b.Release(); ok; v, ok = b.Release() {
		got = append(got, v)
	}
	if want := []int{1, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("Release released %v, want %v", got, want)
	}
	if b.Len() != 0 {
		t.Errorf("Len after releasing everything = %d, want 0", b.Len())
	}
}

// Deadline is when the next window ends, while a value is held.
func TestBuffer_Deadline(t *testing.T) {
	t.Parallel()

	b := reorder.New(cmp.Compare[int], time.Second, 0)
	if _, ok := b.Deadline(); ok {
		t.Error("Deadline reported one with nothing held")
	}
	push(t, b, t0, 2)
	push(t, b, t0.Add(time.Second/2), 1)
	if got, ok := b.Deadline(); !ok || !got.Equal(t0.Add(time.Second)) {
		t.Errorf("Deadline = %v, %t, want %v, true", got, ok, t0.Add(time.Second))
	}
	popAll(b, t0.Add(time.Second))
	if _, ok := b.Deadline(); ok {
		t.Error("Deadline reported one once everything was released")
	}
}

// Whatever is pushed when, every value is either released, in order, or late, and none is released after its window
// ends, nor before unless something it sorts after is due or the limit is passed.
func TestBuffer_Random(t *testing.T) {
	t.Parallel()

	const (
		values = 10000
		window = 10 * time.Millisecond
		limit  = 20
	)
	b := reorder.New(cmp.Compare[int], window, limit)
	now := t0
	pushedAt := make(map[int]time.Time, values)
	var released []int
	pop := func(at time.Time) {
		for r, ok := b.Pop(at); ok; r, ok = b.Pop(at) {
			if at.Sub(pushedAt[r]) > window {
				t.Fatalf("%d released at %v, past its window, pushed at %v", r, at, pushedAt[r])
			}
			released = append(released, r)
		}
	}
	late := 0
	for v := range values {
		next := now.Add(time.Duration(rand.Int64N(int64(window / 4)))) //nolint:gosec // A test needs no cryptographic randomness.
		// As a reader waking at each deadline would.
		for deadline, ok := b.Deadline(); ok && !deadline.After(next); deadline, ok = b.Deadline() {
			pop(deadline)
		}
		now = next
		// Out of order by up to 12 values, about what a window holds.
		value := v + rand.IntN(12) //nolint:gosec // A test needs no cryptographic randomness.
		value = value*values + v   // unique
		pushedAt[value] = now
		if !b.Push(value, now) {
			if last, _ := b.Last(); value >= last {
				t.Fatalf("Push(%d) reported it late, but the last released is %d", value, last)
			}
			late++

			continue
		}
		pop(now)
		if b.Len() > limit {
			t.Fatalf("%d held, past the limit %d", b.Len(), limit)
		}
	}
	for r, ok := b.Release(); ok; r, ok = b.Release() {
		released = append(released, r)
	}

	if !slices.IsSorted(released) {
		t.Error("values were released out of order")
	}
	if len(released)+late != values {
		t.Errorf("%d released and %d late, want %d in all", len(released), late, values)
	}
	if late == 0 || late > values/2 {
		t.Errorf("%d late, want some but not most, so that the test means something", late)
	}
}

// push pushes every value at now, and fails if one is late.
func push[E any](t *testing.T, b *reorder.Buffer[E], now time.Time, values ...E) {
	t.Helper()
	for _, v := range values {
		if !b.Push(v, now) {
			t.Fatalf("Push(%v) reported it late", v)
		}
	}
}

// popAll pops every value due at now.
func popAll[E any](b *reorder.Buffer[E], now time.Time) []E {
	var released []E
	for v, ok := b.Pop(now); ok; v, ok = b.Pop(now) {
		released = append(released, v)
	}

	return released
}
