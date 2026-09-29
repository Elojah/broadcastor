package subscriber

import (
	"context"

	"github.com/google/uuid"
)

// Store is given every message a subscriber loses (see WithStore). Put may be called concurrently.
type Store[T any] interface {
	// Put stores r. ctx has the values of the message's ctx but is never done, so Put must bound itself.
	Put(ctx context.Context, r Record[T]) error
}

// Record is a message a subscriber lost, and Err, the error its error handlers get about it.
type Record[T any] struct {
	SubscriberID uuid.UUID
	Message      T
	Err          error
}
