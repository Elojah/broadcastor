package broadcastor

// Option configures a Broadcastor, when passed to NewBroadcastor.
type Option[T any] func(config *config)

// config is what Options set.
type config struct {
	asyncLimit int
}

// WithAsyncLimit bounds how many async sends (message.WithAsync) are in flight at once, to every subscriber together.
// Past n, an async Broadcast waits for a free slot the way a sync one waits for a subscriber: until its ctx is done or
// the message's timeout runs out, then it reports a *subscriber.TimeoutError. The timeout covers the wait for a slot
// and the send together. 0 or less means no limit, the default.
//
// A stuck subscriber can hold every slot, and so hold up async Broadcasts to every other subscriber. An async
// Broadcast from handle, with no timeout and a ctx that is never done, then waits on itself.
func WithAsyncLimit[T any](n int) Option[T] {
	return func(config *config) {
		config.asyncLimit = n
	}
}
