package holder_test

import (
	"cmp"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/elojah/broadcastor/holder"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

// Reorder releases the messages of a window in ascending order once it ends, d after the first message, each with its
// own ctx, and merges none away. The next message starts another window.
func TestReorder(t *testing.T) {
	t.Parallel()

	r := holder.NewReorder[int](window)
	type key struct{}
	for i, msg := range []int{3, 1, 2} {
		ctx := context.WithValue(t.Context(), key{}, msg)
		hold(t, r, message.Message[int]{Value: msg, Config: message.Config{Context: ctx}}, start.Add(time.Duration(i)*window/4), 0)
	}

	waitFor(t, r, start.Add(window-time.Nanosecond), time.Nanosecond)
	for _, want := range []int{1, 2, 3} {
		m := release(t, r, start.Add(window), false)
		if m.Value != want || m.Config.Context.Value(key{}) != want {
			t.Errorf("Release = %d with ctx %v, want %d with its own", m.Value, m.Config.Context.Value(key{}), want)
		}
	}
	waitFor(t, r, start.Add(window), 0)

	hold(t, r, message.Message[int]{Value: 0}, start.Add(2*window), 0)
	waitFor(t, r, start.Add(2*window), window)
}

// orders are funcs to order by, with what each releases of unordered held together: the messages they find equal in
// the order they came.
var (
	unordered = []int{22, 12, 21, 11}
	orders    = []struct {
		name string
		cmp  func(a, b int) int
		want []int
	}{
		{"ascending", cmp.Compare[int], []int{11, 12, 21, 22}},
		{"descending", func(a, b int) int { return cmp.Compare(b, a) }, []int{22, 21, 12, 11}},
		{"by tens, stable", func(a, b int) int { return cmp.Compare(a/10, b/10) }, []int{12, 11, 22, 21}},
	}
)

// NewReorderFunc orders by cmp.
func TestReorderFunc(t *testing.T) {
	t.Parallel()

	for _, tt := range orders {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := holder.NewReorderFunc(window, tt.cmp)
			for _, msg := range unordered {
				hold(t, r, message.Message[int]{Value: msg}, start, 0)
			}
			if got := releaseAll(r, start.Add(window), false); !slices.Equal(got, tt.want) {
				t.Errorf("released %v, want %v", got, tt.want)
			}
		})
	}
}

// Release with all releases everything held, in order, before the window ends.
func TestReorder_ReleaseAll(t *testing.T) {
	t.Parallel()

	r := holder.NewReorder[int](window)
	for _, msg := range []int{3, 1, 2} {
		hold(t, r, message.Message[int]{Value: msg}, start, 0)
	}
	if got, want := releaseAll(r, start, true), []int{1, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("Release(all) released %v, want %v", got, want)
	}
	waitFor(t, r, start, 0)
}

// With d <= 0, each message is due at once, so it is released alone when the subscriber releases what is due first.
func TestReorder_NoWindow(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -window} {
		r := holder.NewReorder[int](d)
		for _, msg := range []int{2, 1} {
			hold(t, r, message.Message[int]{Value: msg}, start, 0)
			if got := releaseAll(r, start, false); !slices.Equal(got, []int{msg}) {
				t.Errorf("d = %v: released %v, want [%d]", d, got, msg)
			}
		}
	}
}

// releaseAll returns the values h releases at now until it releases nothing.
func releaseAll(h subscriber.Holder[int], now time.Time, all bool) []int {
	var got []int
	for m, _, ok := h.Release(now, all); ok; m, _, ok = h.Release(now, all) {
		got = append(got, m.Value)
	}

	return got
}
