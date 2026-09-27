package broadcastor

import (
	"errors"

	"github.com/google/uuid"
)

// ErrClosed is returned by Subscribe and SubscribeSeq once Close has been called, and by every Close after the first.
var ErrClosed = errors.New("broadcastor closed")

// ErrSubscriberNotFound is matched by *SubscriberNotFoundError, so that errors.Is can tell it apart. The errors about a
// subscriber's messages are in package subscriber.
var ErrSubscriberNotFound = errors.New("subscriber not found")

// SubscriberNotFoundError is what Unsubscribe returns for an ID that is not subscribed, or no longer. It matches
// ErrSubscriberNotFound.
type SubscriberNotFoundError struct {
	SubscriberID uuid.UUID
}

func (e *SubscriberNotFoundError) Error() string {
	return "subscriber not found: " + e.SubscriberID.String()
}

func (e *SubscriberNotFoundError) Is(target error) bool {
	return target == ErrSubscriberNotFound
}
