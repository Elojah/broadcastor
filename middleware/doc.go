// Package middleware holds ready-made subscriber.Middleware for subscriber.WithMiddleware, in the recommended order:
//
//	id, err := b.Subscribe(ctx, handle, subscriber.WithMiddleware(
//		middleware.Recover[string](),
//		middleware.History[string](history),
//		middleware.MaxAge[string](time.Minute, sentAt),
//		middleware.WrapError[string](),
//		middleware.Retry[string](middleware.RetryPolicy{Attempts: 3, Delay: 10 * time.Millisecond}),
//	))
//
// Recover first catches panics in every later middleware. History then records only handled messages, and its failed
// Put is neither wrapped nor retried. An expired message is not retried, and its error already names the subscriber
// and the message. Retry after WrapError retries handle's raw errors, so only the last one is wrapped, and panics are
// not retried.
package middleware
