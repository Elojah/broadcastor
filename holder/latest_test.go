package holder_test

import (
	"context"
	"testing"
	"time"

	"github.com/elojah/multicast/holder"
	"github.com/elojah/multicast/message"
	"github.com/elojah/multicast/subscriber"
)

var (
	_ subscriber.Holder[int] = (*holder.Reduce[int])(nil)
	_ subscriber.Holder[int] = (*holder.Reorder[int])(nil)
	_ subscriber.Holder[int] = (*holder.Jitter[int])(nil)
)

const window = time.Second

// start is when the tests' first window starts: holders read no clock, so any time does.
var start = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// Latest releases the newest message once the window ends, d after the first, with its own ctx, and merges away the
// others. The next message starts another window.
func TestLatest(t *testing.T) {
	t.Parallel()

	l := holder.NewLatest[int](window)
	type key struct{}
	newest := context.WithValue(t.Context(), key{}, "newest")
	hold(t, l, message.Message[int]{Value: 1}, start, 0)
	hold(t, l, message.Message[int]{Value: 2}, start.Add(window/2), 1)
	hold(t, l, message.Message[int]{Value: 3, Config: message.Config{Context: newest}}, start.Add(window-time.Nanosecond), 1)

	waitFor(t, l, start, window)
	waitFor(t, l, start.Add(window-time.Nanosecond), time.Nanosecond)
	if m := release(t, l, start.Add(window), false); m.Value != 3 || m.Config.Context != newest {
		t.Errorf("Release once the window ended = %d with ctx %v, want 3 with its own", m.Value, m.Config.Context)
	}
	waitFor(t, l, start.Add(window), 0)

	hold(t, l, message.Message[int]{Value: 4}, start.Add(2*window), 0)
	waitFor(t, l, start.Add(2*window), window)
}

// Release with all releases the message held before its window ends.
func TestLatest_ReleaseAll(t *testing.T) {
	t.Parallel()

	l := holder.NewLatest[int](window)
	hold(t, l, message.Message[int]{Value: 1}, start, 0)
	if m := release(t, l, start, true); m.Value != 1 {
		t.Errorf("Release(all) = %d, want 1", m.Value)
	}
	waitFor(t, l, start, 0)
}

// With d <= 0, a message is due at once.
func TestLatest_NoWindow(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -window} {
		l := holder.NewLatest[int](d)
		hold(t, l, message.Message[int]{Value: 1}, start, 0)
		if m := release(t, l, start, false); m.Value != 1 {
			t.Errorf("d = %v: Release = %d, want 1", d, m.Value)
		}
	}
}

// hold holds m at now, which must be taken with merged messages merged away.
func hold(t *testing.T, h subscriber.Holder[int], m message.Message[int], now time.Time, merged int) {
	t.Helper()
	if got, ok := h.Hold(m, now); !ok || got != merged {
		t.Errorf("Hold(%d) = %d, %t, want %d, true", m.Value, got, ok, merged)
	}
}

// release returns what h releases at now, which must be something.
func release(t *testing.T, h subscriber.Holder[int], now time.Time, all bool) message.Message[int] {
	t.Helper()
	m, wait, ok := h.Release(now, all)
	if !ok {
		t.Fatalf("Release(all: %t) released nothing, waiting %v", all, wait)
	}

	return m
}

// waitFor checks that h releases nothing at now, and waits wait for the next message, 0 if none is held.
func waitFor(t *testing.T, h subscriber.Holder[int], now time.Time, wait time.Duration) {
	t.Helper()
	if m, got, ok := h.Release(now, false); ok || got != wait {
		t.Errorf("Release = %d, %v, %t, want nothing, waiting %v", m.Value, got, ok, wait)
	}
}
