package subscriber

import (
	"context"
	"slices"
)

// Handler handles one message: the handle function given to Subscribe, and what a Middleware wraps.
type Handler[T any] func(ctx context.Context, id uint64, msg T) error

// Middleware wraps a Handler, for WithMiddleware. Package middleware holds ready-made ones.
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
