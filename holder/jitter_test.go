package holder_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/elojah/broadcastor/v2/holder"
	"github.com/elojah/broadcastor/v2/message"
)

// Jitter holds each message for d from when it came, and once one is due, releases it after the messages held
// ordered before it, each with its own ctx, merging none away. A message coming after the ones before it were released
// is still ordered with those it came within d of.
func TestJitter(t *testing.T) {
	t.Parallel()

	j := holder.NewJitter[int](window)
	type key struct{}
	holdAt := func(msg int, at time.Duration) {
		t.Helper()
		ctx := context.WithValue(t.Context(), key{}, msg)
		hold(t, j, message.Message[int]{Value: msg, Config: message.Config{Context: ctx}}, start.Add(at), 0)
	}
	releaseAt := func(at time.Duration, want ...int) {
		t.Helper()
		for _, want := range want {
			m := release(t, j, start.Add(at), false)
			if m.Value != want || m.Config.Context.Value(key{}) != want {
				t.Errorf("Release at %v = %d with ctx %v, want %d with its own", at, m.Value, m.Config.Context.Value(key{}), want)
			}
		}
	}

	holdAt(20, 0)
	holdAt(10, window/2)
	holdAt(30, 3*window/4)
	waitFor(t, j, start.Add(window-time.Nanosecond), time.Nanosecond)
	// 20 is due, and 10 goes first.
	releaseAt(window, 10, 20)
	waitFor(t, j, start.Add(window), 3*window/4)

	// 25 comes once 20 is gone, but within d of 30.
	holdAt(25, 3*window/2)
	waitFor(t, j, start.Add(3*window/2), window/4)
	releaseAt(7*window/4, 25, 30)
	waitFor(t, j, start.Add(7*window/4), 0)
}

// NewJitterFunc orders by cmp.
func TestJitterFunc(t *testing.T) {
	t.Parallel()

	for _, tt := range orders {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			j := holder.NewJitterFunc(window, tt.cmp)
			for _, msg := range unordered {
				hold(t, j, message.Message[int]{Value: msg}, start, 0)
			}
			if got := releaseAll(j, start.Add(window), false); !slices.Equal(got, tt.want) {
				t.Errorf("released %v, want %v", got, tt.want)
			}
		})
	}
}

// A stream of messages each ordered before the one before holds none for more than d: once the oldest is due, the
// newer ones go first.
func TestJitter_Stream(t *testing.T) {
	t.Parallel()

	j := holder.NewJitter[int](window)
	came := map[int]time.Time{}
	var got []int
	for i := range 12 {
		now := start.Add(time.Duration(i) * window / 4)
		for _, msg := range releaseAll(j, now, false) {
			if held := now.Sub(came[msg]); held > window {
				t.Errorf("%d held for %v, want at most %v", msg, held, window)
			}
			got = append(got, msg)
		}
		msg := 100 - i
		came[msg] = now
		hold(t, j, message.Message[int]{Value: msg}, now, 0)
	}
	got = append(got, releaseAll(j, start.Add(3*window), false)...)
	if want := []int{97, 98, 99, 100, 93, 94, 95, 96, 89, 90, 91, 92}; !slices.Equal(got, want) {
		t.Errorf("released %v, want %v", got, want)
	}
}

// Release with all releases everything held, in order, before any is due.
func TestJitter_ReleaseAll(t *testing.T) {
	t.Parallel()

	j := holder.NewJitter[int](window)
	for _, msg := range []int{3, 1, 2} {
		hold(t, j, message.Message[int]{Value: msg}, start, 0)
	}
	if got, want := releaseAll(j, start, true), []int{1, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("Release(all) released %v, want %v", got, want)
	}
	waitFor(t, j, start, 0)
}

// With d <= 0, each message is due at once, so it is released alone when the subscriber releases what is due first.
func TestJitter_NoWindow(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -window} {
		j := holder.NewJitter[int](d)
		for _, msg := range []int{2, 1} {
			hold(t, j, message.Message[int]{Value: msg}, start, 0)
			if got := releaseAll(j, start, false); !slices.Equal(got, []int{msg}) {
				t.Errorf("d = %v: released %v, want [%d]", d, got, msg)
			}
		}
	}
}

// Once it has grown to what a steady stream holds, a Jitter allocates nothing, though it never empties. Not parallel:
// AllocsPerRun counts every goroutine's allocations.
func TestJitter_Allocs(t *testing.T) { //nolint:paralleltest // AllocsPerRun counts every goroutine's allocations
	j := holder.NewJitter[int](window)
	now, i := start, 0
	step := func() {
		for {
			if _, _, ok := j.Release(now, false); !ok {
				break
			}
		}
		j.Hold(message.Message[int]{Value: i * 7 % 5}, now)
		now, i = now.Add(window/4), i+1
	}
	for range 10 {
		step()
	}
	if allocs := testing.AllocsPerRun(100, step); allocs != 0 {
		t.Errorf("a message allocates %v times, want 0", allocs)
	}
}
