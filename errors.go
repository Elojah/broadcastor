package multicast

import (
	"errors"
	"strconv"
)

// ErrClosed is returned by Subscribe and SubscribeSeq after Close, and by every Close or Shutdown after the first.
var ErrClosed = errors.New("group closed")

// ErrSubscriberNotFound is matched by *SubscriberNotFoundError.
var ErrSubscriberNotFound = errors.New("subscriber not found")

// SubscriberNotFoundError is what Unsubscribe returns for an unknown ID.
type SubscriberNotFoundError struct {
	SubscriberID uint64
}

func (e *SubscriberNotFoundError) Error() string {
	return "subscriber not found: " + strconv.FormatUint(e.SubscriberID, 10)
}

func (e *SubscriberNotFoundError) Is(target error) bool {
	return target == ErrSubscriberNotFound
}
