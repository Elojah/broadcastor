// Package middleware holds ready-made subscriber.Middleware, to pass to subscriber.WithMiddleware:
//
//	id, err := b.Subscribe(ctx, handle, subscriber.WithMiddleware(
//		middleware.Recover[string](),
//		middleware.WrapError[string](),
//		middleware.Retry[string](middleware.RetryPolicy{Attempts: 3, Delay: 10 * time.Millisecond}),
//	))
//
// Given first, in that order, Recover also recovers panics in every later middleware, and the *subscriber.PanicError it
// returns is not wrapped in a *subscriber.HandleError. Retry then retries the errors handle returns as is, wrapping
// only the last one, and does not retry panics.
package middleware
