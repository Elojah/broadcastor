package holder_test

import (
	"context"
	"iter"
	"slices"
	"testing"
	"time"

	"github.com/elojah/multicast/holder"
	"github.com/elojah/multicast/message"
)

// Batch releases the messages of a window as one batch, in order, once it ends, d after the first message, with the
// newest message's ctx, and merges none away. The next message starts another window, with a batch of its own.
func TestBatch(t *testing.T) {
	t.Parallel()

	b := holder.NewBatch[int](window, 0)
	type key struct{}
	newest := context.WithValue(t.Context(), key{}, "newest")
	for i, msg := range []message.Message[int]{{Value: 3}, {Value: 1}, {Value: 2, Config: message.Config{Context: newest}}} {
		hold(t, b, msg, start.Add(time.Duration(i)*window/4), 0)
	}

	waitFor(t, b, start.Add(window-time.Nanosecond), time.Nanosecond)
	first := release(t, b, start.Add(window), false)
	if got, want := values(first), []int{3, 1, 2}; !slices.Equal(got, want) || first.Config.Context != newest {
		t.Errorf("Release = %v with ctx %v, want %v with the newest message's", got, first.Config.Context, want)
	}
	waitFor(t, b, start.Add(window), 0)

	hold(t, b, message.Message[int]{Value: 4}, start.Add(2*window), 0)
	waitFor(t, b, start.Add(2*window), window)
	second := release(t, b, start.Add(3*window), false)
	if got := values(second); !slices.Equal(got, []int{4}) || second.Batch == first.Batch {
		t.Errorf("the next window released %v in batch %p, want [4] in a batch other than the first's", got, second.Batch)
	}
	if got := values(first); !slices.Equal(got, []int{3, 1, 2}) {
		t.Errorf("the first batch holds %v once the next window ended, want it unchanged", got)
	}
}

// A window holding n messages is due at once, so the subscriber releases it before holding the next message.
func TestBatch_Size(t *testing.T) {
	t.Parallel()

	b := holder.NewBatch[int](window, 2)
	hold(t, b, message.Message[int]{Value: 1}, start, 0)
	waitFor(t, b, start, window)
	hold(t, b, message.Message[int]{Value: 2}, start.Add(window/4), 0)
	if got := values(release(t, b, start.Add(window/4), false)); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("Release once full = %v, want [1 2]", got)
	}
	waitFor(t, b, start.Add(window/4), 0)

	hold(t, b, message.Message[int]{Value: 3}, start.Add(window/2), 0)
	waitFor(t, b, start.Add(window/2), window)
}

// Release(all) releases the window's batch at once.
func TestBatch_ReleaseAll(t *testing.T) {
	t.Parallel()

	b := holder.NewBatch[int](window, 0)
	for _, msg := range []int{1, 2} {
		hold(t, b, message.Message[int]{Value: msg}, start, 0)
	}
	if got := values(release(t, b, start, true)); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("Release(all) = %v, want [1 2]", got)
	}
	waitFor(t, b, start, 0)
}

// With d <= 0, each message is due at once, so it is released alone when the subscriber releases what is due first.
func TestBatch_NoWindow(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -window} {
		b := holder.NewBatch[int](d, 0)
		for _, msg := range []int{2, 1} {
			hold(t, b, message.Message[int]{Value: msg}, start, 0)
			if got := values(release(t, b, start, false)); !slices.Equal(got, []int{msg}) {
				t.Errorf("d = %v: released %v, want [%d]", d, got, msg)
			}
			waitFor(t, b, start, 0)
		}
	}
}

// NewBatchFunc releases the batches newBatch makes, one per window.
func TestBatchFunc(t *testing.T) {
	t.Parallel()

	made := 0
	b := holder.NewBatchFunc(window, 0, func() message.Batch[int] {
		made++

		return &sum{}
	})
	for _, msg := range []int{1, 2, 3} {
		hold(t, b, message.Message[int]{Value: msg}, start, 0)
	}
	m := release(t, b, start, true)
	if s, ok := m.Batch.(*sum); !ok || s.total != 6 || made != 1 {
		t.Errorf("Release = %#v after %d batches made, want a *sum of 6 after 1", m.Batch, made)
	}
}

// values returns the values of m's batch, which it must carry.
func values(m message.Message[int]) []int {
	if m.Batch == nil {
		return nil
	}

	return slices.Collect(m.Batch.All())
}

// sum is a batch of its own type, adding up what it holds.
type sum struct {
	values []int
	total  int
}

func (s *sum) Add(msg int) {
	s.values = append(s.values, msg)
	s.total += msg
}

func (s *sum) All() iter.Seq[int] {
	return slices.Values(s.values)
}

func (s *sum) Len() int {
	return len(s.values)
}

func (s *sum) New() message.Batch[int] {
	return &sum{}
}
