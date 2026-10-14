package subscriber

import (
	"time"

	"github.com/elojah/broadcastor/message"
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
