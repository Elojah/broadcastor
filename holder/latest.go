package holder

import (
	"time"

	"github.com/elojah/broadcastor/message"
)

// Latest is a subscriber.Holder that holds a message for d, replacing it with each newer one, then releases it: of
// the messages a window takes, only the last reaches handle. Create one with NewLatest.
type Latest[T any] struct {
	d       time.Duration
	held    message.Message[T]
	due     time.Time
	holding bool
}

// NewLatest returns a Latest whose windows last d. With d <= 0, each message is released at once.
func NewLatest[T any](d time.Duration) *Latest[T] {
	return &Latest[T]{d: d}
}

// Hold replaces the message held with m, merging it away, or else starts a window with m.
func (l *Latest[T]) Hold(m message.Message[T], now time.Time) (int, bool) {
	if l.holding {
		l.held = m

		return 1, true
	}
	l.held, l.due, l.holding = m, now.Add(l.d), true

	return 0, true
}

// Release returns the message held once its window has ended at now, or at once if all.
func (l *Latest[T]) Release(now time.Time, all bool) (message.Message[T], time.Duration, bool) {
	if !l.holding {
		return message.Message[T]{}, 0, false
	}
	if wait := l.due.Sub(now); wait > 0 && !all {
		return message.Message[T]{}, wait, false
	}
	m := l.held
	l.held, l.holding = message.Message[T]{}, false

	return m, 0, true
}
