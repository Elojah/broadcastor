// Package filter holds filters for subscriber.WithFilter, for readings that repeat themselves:
//
//	id, err := b.Subscribe(ctx, handle, subscriber.WithFilter(filter.Changed(func(prev, next float64) bool {
//		return math.Abs(next-prev) >= 0.5
//	})))
//
// Each filter keeps state, so give each subscriber its own. Concurrent Broadcasts may call it in any order, so the last
// value it passed is the last to reach it, not necessarily the last broadcast.
package filter

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Changed returns a filter that passes the first value, then each one changed reports far enough from the last value
// passed, not the last seen: a deadband, so a slow drift passes once it has moved far enough. changed runs under a
// mutex.
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

// Every returns a filter that passes at most one value per d: the first, then the first one d after the last passed.
// d <= 0 passes every value.
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
