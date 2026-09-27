package subscriber

import (
	"context"
	"slices"

	"github.com/google/uuid"
)

// Handler is what a subscriber calls for every message it takes, with the ctx passed to Subscribe and its own ID: the
// handle function given to Subscribe, and what a Middleware wraps.
type Handler[T any] func(ctx context.Context, id uuid.UUID, msg T) error

// Middleware wraps a Handler in another, which may do something before or after calling next, change the error next
// returns, call it again, or not call it at all. Pass it to WithMiddleware. Package middleware holds ready-made ones.
type Middleware[T any] func(next Handler[T]) Handler[T]

// chain wraps handle in middlewares, the first one outermost, skipping nil ones.
func chain[T any](handle Handler[T], middlewares ...Middleware[T]) Handler[T] {
	for _, middleware := range slices.Backward(middlewares) {
		if middleware != nil {
			handle = middleware(handle)
		}
	}

	return handle
}
