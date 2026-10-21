package subscriber

import (
	"time"

	"github.com/elojah/multicast/message"
)

// Holder holds the messages a subscriber takes until they are due (WithHolder). Package holder holds ready-made ones.
// Only the subscriber's goroutine calls it, so it needs no lock, and each call passes the time to decide at, so it
// reads no clock.
type Holder[T any] interface {
	// Hold takes m at now, once Release has returned everything due at now. m is a single value: the subscriber holds
	// a batch's one at a time. It returns how many of the messages held, m included, it merged away, or false if it had
	// no room, m being dropped. To keep m instead, it can make a message it holds due at now.
	Hold(m message.Message[T], now time.Time) (int, bool)

	// Release removes and returns a message due at now, or any one if all. Otherwise it returns false and how long
	// until one is due, or 0 if none will be until the next message, as when it holds none. It may return messages it
	// holds as one batch (message.Message.Batch), which then go to the batch handler (WithBatchHandler).
	Release(now time.Time, all bool) (message.Message[T], time.Duration, bool)
}

// pace is Consume's loop with a holder: it holds each message taken, and processes each one released once due, then
// everything still held once ch is closed. Only it calls the holder.
func (s *Subscriber[T]) pace(handle Handler[T]) {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		// A stale tick, from a timer that fired while a message came, only wakes the loop up once more.
		var due <-chan time.Time
		if wait := s.unhold(handle, time.Now(), false); wait > 0 {
			if timer == nil {
				timer = time.NewTimer(wait)
			} else {
				timer.Reset(wait)
			}
			due = timer.C
		}
		select {
		case m, ok := <-s.inbox.ch:
			if !ok {
				s.unhold(handle, time.Now(), true)

				return
			}
			s.counters.took(m.Len())
			s.hold(handle, m, time.Now())
		case <-due:
		}
	}
}

// hold passes m to the holder at now, a batch one value at a time, or reports it once the subscriber discards. Before
// each value, it releases what is due at now, which may have come due while waiting or as the holder took the previous
// one, so that the holder never gets a message for a window that ended.
func (s *Subscriber[T]) hold(handle Handler[T], m message.Message[T], now time.Time) {
	s.unhold(handle, now, false)
	if s.discarding.Load() {
		s.closed(m)

		return
	}
	if m.Batch != nil {
		// Called rather than ranged over, which would allocate the loop's state too.
		m.Batch.All()(func(value T) bool {
			s.hold(handle, message.Message[T]{Value: value, Config: m.Config}, now)

			return true
		})

		return
	}
	merged, ok := s.config.holder.Hold(m, now)
	if !ok {
		s.drop(m)

		return
	}
	s.counters.hold(merged)
}

// unhold processes each message the holder releases, every one if all, else those due at now, or reports them once the
// subscriber discards. It returns how long until the next one is due, 0 if none will be until the next message.
func (s *Subscriber[T]) unhold(handle Handler[T], now time.Time, all bool) time.Duration {
	for {
		m, wait, ok := s.config.holder.Release(now, all)
		if !ok {
			return wait
		}
		s.counters.unhold(m.Len())
		if s.discarding.Load() {
			s.closed(m)

			continue
		}
		s.process(handle, m)
	}
}
