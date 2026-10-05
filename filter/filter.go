// Package filter holds filters for subscriber.WithFilter, for readings that repeat themselves, such as a sensor that
// sends the same temperature every second:
//
//	id, err := b.Subscribe(ctx, handle, subscriber.WithFilter(filter.Changed(func(prev, next float64) bool {
//		return math.Abs(next-prev) >= 0.5
//	})))
//
// A filter keeps state of its own, so give each subscriber its own. Concurrent Broadcasts may call a filter at once,
// and in any order, so the last value it passed is the last to reach it, which may not be the last broadcast.
package filter

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Changed returns a filter that passes the first value, then each value that changed reports as far enough from the
// last value passed. Comparing with the last value passed, rather than the last one seen, makes it a deadband: a value
// that drifts slowly passes once it has moved far enough. changed runs under a mutex, one call at a time.
func Changed[T any](changed func(prev, next T) bool) func(T) bool {
	var (
		mu     sync.Mutex
		last   T
		passed bool
	)

	return func(next T) bool {
		mu.Lock()
		defer mu.Unlock()
		if passed && !changed(last, next) {
			return false
		}
		last, passed = next, true

		return true
	}
}

// Every returns a filter that passes the first value, then the first one at least d after the last one it passed, so
// at most one per d. 0 or less passes every value.
func Every[T any](d time.Duration) func(T) bool {
	if d <= 0 {
		return func(T) bool { return true }
	}
	start := time.Now()
	// next is when the next value may pass, as time since start.
	var next atomic.Int64

	return func(T) bool {
		now := int64(time.Since(start))
		n := next.Load()
		if now < n {
			return false
		}
		after := now + int64(d)
		if after < now {
			after = math.MaxInt64
		}

		// Of the calls racing past n, only one passes.
		return next.CompareAndSwap(n, after)
	}
}
