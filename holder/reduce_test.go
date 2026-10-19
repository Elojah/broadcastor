package holder_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/elojah/broadcastor/v2/holder"
	"github.com/elojah/broadcastor/v2/message"
)

// latestByTens keeps the latest message of each ten, in place, so that 11, 12 and 13 share a slot.
func latestByTens(held []int, next int) []int {
	for i, msg := range held {
		if msg/10 == next/10 {
			held[i] = next

			return held
		}
	}

	return append(held, next)
}

// Reduce releases what reduce left, in order, once the window ends, d after the first message, each with the newest
// message's ctx. Hold counts what reduce merged away. The next message starts another window.
func TestReduce(t *testing.T) {
	t.Parallel()

	r := holder.NewReduce(window, latestByTens)
	type key struct{}
	newest := context.WithValue(t.Context(), key{}, "newest")
	for i, tt := range []struct {
		msg    message.Message[int]
		merged int
	}{
		{message.Message[int]{Value: 11}, 0},
		{message.Message[int]{Value: 21}, 0},
		{message.Message[int]{Value: 12}, 1},
		{message.Message[int]{Value: 13, Config: message.Config{Context: newest}}, 1},
	} {
		hold(t, r, tt.msg, start.Add(time.Duration(i)*window/4), tt.merged)
	}

	waitFor(t, r, start.Add(window-time.Nanosecond), time.Nanosecond)
	for _, want := range []int{13, 21} {
		if m := release(t, r, start.Add(window), false); m.Value != want || m.Config.Context != newest {
			t.Errorf("Release = %d with ctx %v, want %d with the newest message's", m.Value, m.Config.Context, want)
		}
	}
	waitFor(t, r, start.Add(window), 0)

	hold(t, r, message.Message[int]{Value: 31}, start.Add(2*window), 0)
	waitFor(t, r, start.Add(2*window), window)
}

// reduce decides what a window releases: fewer messages, but not only one, or one, or none.
func TestReduce_Reducers(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		reduce func(held []int, next int) []int
		want   []int
	}{
		{"dedupe", func(held []int, next int) []int {
			if slices.Contains(held, next) {
				return held
			}

			return append(held, next)
		}, []int{11, 21, 12, 22, 13}},
		{"latest by key", latestByTens, []int{13, 22}},
		{"sum", func(held []int, next int) []int {
			if len(held) == 0 {
				return append(held, next)
			}
			held[0] += next

			return held
		}, []int{90}},
		{"drop odd", func(held []int, next int) []int {
			if next%2 == 1 {
				return held
			}

			return append(held, next)
		}, []int{12, 22}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := holder.NewReduce(window, tt.reduce)
			msgs := []int{11, 21, 11, 12, 22, 13}
			merged := 0
			for _, msg := range msgs {
				n, ok := r.Hold(message.Message[int]{Value: msg}, start)
				if !ok {
					t.Fatalf("Hold(%d) had no room", msg)
				}
				merged += n
			}
			var got []int
			for m, _, ok := r.Release(start.Add(window), false); ok; m, _, ok = r.Release(start.Add(window), false) {
				got = append(got, m.Value)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("released %v, want %v", got, tt.want)
			}
			if want := len(msgs) - len(tt.want); merged != want {
				t.Errorf("Hold merged %d messages away, want %d", merged, want)
			}
		})
	}
}

// Release with all releases everything held, in order, before the window ends.
func TestReduce_ReleaseAll(t *testing.T) {
	t.Parallel()

	r := holder.NewReduce(window, latestByTens)
	for _, msg := range []int{11, 21, 12} {
		r.Hold(message.Message[int]{Value: msg}, start)
	}
	for _, want := range []int{12, 21} {
		if m := release(t, r, start, true); m.Value != want {
			t.Errorf("Release(all) = %d, want %d", m.Value, want)
		}
	}
	waitFor(t, r, start, 0)
}

// With d <= 0, each message is due at once, and reduce sees it alone when the subscriber releases what is due first.
func TestReduce_NoWindow(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -window} {
		r := holder.NewReduce(d, func(held []int, next int) []int {
			if len(held) != 0 {
				t.Errorf("d = %v: reduce got %v held, want none", d, held)
			}

			return append(held, next)
		})
		for _, msg := range []int{1, 1} {
			hold(t, r, message.Message[int]{Value: msg}, start, 0)
			if m := release(t, r, start, false); m.Value != msg {
				t.Errorf("d = %v: Release = %d, want %d", d, m.Value, msg)
			}
		}
	}
}
