// Package filter holds filters for subscriber.WithFilter, for readings that repeat themselves:
//
//	id, err := b.Subscribe(ctx, handle, subscriber.WithFilter(filter.Changed(func(prev, next float64) bool {
//		return math.Abs(next-prev) >= 0.5
//	})))
//
// Each filter keeps state, so give each subscriber its own, and WithKey keeps it per key, such as a sensor ID.
// Concurrent Broadcasts may call it in any order, so the last value it passed is the last to reach it, not necessarily
// the last broadcast.
package filter

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Option configures a filter, when passed to Changed or Every.
type Option[T any] func(config *config[T])

// config is what Options set.
type config[T any] struct {
	heartbeat      time.Duration
	heartbeatCount int

	// split, set by WithKey, gives each key a filter of its own, built by newFilter.
	split func(newFilter func() func(T) bool) func(T) bool
}

// build returns newFilter's filter, or one per key with WithKey.
func (c *config[T]) build(newFilter func() func(T) bool) func(T) bool {
	if c.split != nil {
		return c.split(newFilter)
	}

	return newFilter()
}

// cut is the last value a deadband passed. A pass replaces it whole, so that a value is compared without a lock.
type cut[T any] struct {
	last T
	// at is set only with an interval, so that without one no clock is read.
	at time.Time
	// seen counts the values compared with last, with a heartbeat count.
	seen atomic.Int64
}

// due reports whether a value changed rejects passes anyway, on a heartbeat.
func (c *cut[T]) due(interval time.Duration, count int) bool {
	if interval > 0 && time.Since(c.at) >= interval {
		return true
	}

	return count > 0 && c.seen.Add(1) >= int64(count)
}

// WithKey gives each key a filter of its own, with its own last value, period and heartbeats. The filter keeps an entry
// for each key it has seen, for good, so keys are meant to be few, such as sensor IDs.
func WithKey[T any, K comparable](key func(msg T) K) Option[T] {
	return func(config *config[T]) {
		config.split = func(newFilter func() func(T) bool) func(T) bool {
			// Each key's entry is written once, then only read: what sync.Map is for.
			var filters sync.Map

			return func(next T) bool {
				k := key(next)
				f, ok := filters.Load(k)
				if !ok {
					// Of the values racing in with a new key, each goes through the filter stored first.
					f, _ = filters.LoadOrStore(k, newFilter())
				}
				pass, _ := f.(func(T) bool)

				return pass(next)
			}
		}
	}
}

// WithHeartbeat passes a value once d has gone by since the last one passed, whatever the filter says, so that a
// steady reading still shows. It passes only values that come: a sensor gone quiet stays quiet. d <= 0 sets none.
func WithHeartbeat[T any](d time.Duration) Option[T] {
	return func(config *config[T]) {
		config.heartbeat = d
	}
}

// WithHeartbeatCount passes the n-th value since the last one passed, whatever the filter says. n <= 0 sets none.
func WithHeartbeatCount[T any](n int) Option[T] {
	return func(config *config[T]) {
		config.heartbeatCount = n
	}
}

// Changed returns a filter that passes the first value, then each one changed reports far enough from the last value
// passed, not the last seen: a deadband, so a slow drift passes once it has moved far enough. changed may run
// concurrently, and more than once for one value, so it must only compare prev and next.
func Changed[T any](changed func(prev, next T) bool, options ...Option[T]) func(T) bool {
	var config config[T]
	for _, option := range options {
		option(&config)
	}

	return config.build(func() func(T) bool { return deadband(changed, config.heartbeat, config.heartbeatCount) })
}

// Every returns a filter that passes at most one value per d: the first, then the first one d after the last passed.
// d <= 0 passes every value. A heartbeat shorter than d shortens it.
func Every[T any](d time.Duration, options ...Option[T]) func(T) bool {
	if d <= 0 {
		return func(T) bool { return true }
	}
	var config config[T]
	for _, option := range options {
		option(&config)
	}
	if config.heartbeat > 0 {
		d = min(d, config.heartbeat)
	}
	if config.heartbeatCount > 0 {
		// every has no room for a count, and a deadband that never changes, with d as its heartbeat, is the same.
		return config.build(func() func(T) bool {
			return deadband(func(T, T) bool { return false }, d, config.heartbeatCount)
		})
	}

	return config.build(func() func(T) bool { return every[T](d) })
}

// deadband is Changed for one key, with heartbeats of interval and count.
func deadband[T any](changed func(prev, next T) bool, interval time.Duration, count int) func(T) bool {
	var last atomic.Pointer[cut[T]]

	return func(next T) bool {
		for {
			c := last.Load()
			if c != nil && !changed(c.last, next) && !c.due(interval, count) {
				return false
			}
			passed := &cut[T]{last: next}
			if interval > 0 {
				passed.at = time.Now()
			}
			// Of the values racing to replace c, one passes, and the others are compared again with it.
			if last.CompareAndSwap(c, passed) {
				return true
			}
		}
	}
}

// every is Every for one key, without a heartbeat count.
func every[T any](d time.Duration) func(T) bool {
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
