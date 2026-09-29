package broadcastor

import (
	"errors"

	"github.com/google/uuid"
)

// ErrClosed is returned by Subscribe and SubscribeSeq after Close, and by every Close after the first.
var ErrClosed = errors.New("broadcastor closed")

// ErrSubscriberNotFound is matched by *SubscriberNotFoundError.
var ErrSubscriberNotFound = errors.New("subscriber not found")

// SubscriberNotFoundError is what Unsubscribe returns for an unknown ID.
type SubscriberNotFoundError struct {
	SubscriberID uuid.UUID
}

func (e *SubscriberNotFoundError) Error() string {
	return "subscriber not found: " + e.SubscriberID.String()
}

func (e *SubscriberNotFoundError) Is(target error) bool {
	return target == ErrSubscriberNotFound
}
