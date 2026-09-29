// Package middleware holds ready-made subscriber.Middleware, to pass to subscriber.WithMiddleware:
//
//	id, err := b.Subscribe(ctx, handle, subscriber.WithMiddleware(
//		middleware.Recover[string](),
//		middleware.WrapError[string](),
//		middleware.Retry[string](middleware.RetryPolicy{Attempts: 3, Delay: 10 * time.Millisecond}),
//	))
//
// Give them in that order: Recover first also catches panics in later middlewares, and Retry after WrapError sees
// handle's raw errors, so only the last one is wrapped and panics are not retried.
package middleware
