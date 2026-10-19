package filter_test

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elojah/broadcastor/v2/filter"
)

// Changed passes the first value, then each one at least 5 from the last one it passed: 15 passes although 12 and 14
// were each closer than 5 to the value before them.
func TestChanged(t *testing.T) {
	t.Parallel()

	pass := filter.Changed(func(prev, next int) bool { return max(next-prev, prev-next) >= 5 })
	values := []int{10, 12, 14, 15, 11, 10, 13}
	got := make([]bool, 0, len(values))
	for _, v := range values {
		got = append(got, pass(v))
	}
	if want := []bool{true, false, false, true, false, true, false}; !slices.Equal(got, want) {
		t.Errorf("Changed passed %v of %v, want %v", got, values, want)
	}
}

// Of values reaching Changed at once, the first passes and the others are compared with it.
func TestChanged_Concurrent(t *testing.T) {
	t.Parallel()

	pass := filter.Changed(func(int, int) bool { return false })
	var (
		passed atomic.Int64
		wg     sync.WaitGroup
	)
	for v := range 100 {
		wg.Go(func() {
			if pass(v) {
				passed.Add(1)
			}
		})
	}
	wg.Wait()
	if n := passed.Load(); n != 1 {
		t.Errorf("Changed passed %d values, want only the first", n)
	}
}

// Every passes the first value, then the first one at least d after the last one it passed.
func TestEvery(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		pass := filter.Every[int](time.Second)
		// At 0, 0.5s, 1s, 1.5s, 2.5s and 3s.
		waits := []time.Duration{0, 500 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond, time.Second, 500 * time.Millisecond}
		got := make([]bool, 0, len(waits))
		for _, wait := range waits {
			time.Sleep(wait)
			got = append(got, pass(0))
		}
		if want := []bool{true, false, true, false, true, false}; !slices.Equal(got, want) {
			t.Errorf("Every passed %v, want %v", got, want)
		}
	})
}

// Of the values reaching Every at once, one passes per period.
func TestEvery_Concurrent(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		pass := filter.Every[int](time.Second)
		for period := range 2 {
			var (
				passed atomic.Int64
				wg     sync.WaitGroup
			)
			for v := range 100 {
				wg.Go(func() {
					if pass(v) {
						passed.Add(1)
					}
				})
			}
			wg.Wait()
			if n := passed.Load(); n != 1 {
				t.Errorf("Every passed %d values in period %d, want 1", n, period)
			}
			time.Sleep(time.Second)
		}
	})
}

// Every with 0 or less passes every value.
func TestEvery_Zero(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -time.Second} {
		pass := filter.Every[int](d)
		for v := range 3 {
			if !pass(v) {
				t.Errorf("Every(%v) did not pass value %d, want every value passed", d, v)
			}
		}
	}
}

// reading is a sensor's, for the keyed filters.
type reading struct {
	sensor string
	value  int
}

func bySensor(r reading) string { return r.sensor }

// passes runs pass over values, in order, and returns what it passed.
func passes[T any](pass func(T) bool, values ...T) []bool {
	got := make([]bool, 0, len(values))
	for _, v := range values {
		got = append(got, pass(v))
	}

	return got
}

// With WithKey, Changed compares each sensor's value with that sensor's last one passed.
func TestChanged_WithKey(t *testing.T) {
	t.Parallel()

	pass := filter.Changed(func(prev, next reading) bool { return max(next.value-prev.value, prev.value-next.value) >= 5 }, filter.WithKey(bySensor))
	got := passes(pass, reading{"a", 10}, reading{"b", 20}, reading{"a", 15}, reading{"b", 22}, reading{"b", 25}, reading{"a", 16})
	if want := []bool{true, true, true, false, true, false}; !slices.Equal(got, want) {
		t.Errorf("Changed with WithKey passed %v, want %v", got, want)
	}
}

// Of values reaching Changed at once with WithKey, the first of each key passes.
func TestChanged_WithKey_Concurrent(t *testing.T) {
	t.Parallel()

	pass := filter.Changed(func(reading, reading) bool { return false }, filter.WithKey(bySensor))
	var (
		passed atomic.Int64
		wg     sync.WaitGroup
	)
	for v := range 100 {
		wg.Go(func() {
			if pass(reading{[]string{"a", "b"}[v%2], v}) {
				passed.Add(1)
			}
		})
	}
	wg.Wait()
	if n := passed.Load(); n != 2 {
		t.Errorf("Changed with WithKey passed %d values, want the first of each of 2 keys", n)
	}
}

// With WithKey, Every passes one value per d for each sensor.
func TestEvery_WithKey(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		pass := filter.Every(time.Second, filter.WithKey(bySensor))
		got := passes(pass, reading{"a", 0}, reading{"b", 0}, reading{"a", 1})
		time.Sleep(time.Second)
		got = append(got, passes(pass, reading{"a", 2}, reading{"b", 2})...)
		if want := []bool{true, true, false, true, true}; !slices.Equal(got, want) {
			t.Errorf("Every with WithKey passed %v, want %v", got, want)
		}
	})
}

// With WithHeartbeat, Changed passes an unchanged value once d has gone by since the last one passed, changed or not.
func TestChanged_WithHeartbeat(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		pass := filter.Changed(func(prev, next int) bool { return next != prev }, filter.WithHeartbeat[int](time.Second))
		// 0 at 0, 0.5s and 1s, 1 at 1.5s, then 1 at 2s and 2.5s.
		values := []int{0, 0, 0, 1, 1, 1}
		got := make([]bool, 0, len(values))
		for i, v := range values {
			if i > 0 {
				time.Sleep(500 * time.Millisecond)
			}
			got = append(got, pass(v))
		}
		if want := []bool{true, false, true, true, false, true}; !slices.Equal(got, want) {
			t.Errorf("Changed with WithHeartbeat passed %v of %v, want %v", got, values, want)
		}
	})
}

// With WithHeartbeatCount, Changed passes the n-th value since the last one passed, changed or not.
func TestChanged_WithHeartbeatCount(t *testing.T) {
	t.Parallel()

	pass := filter.Changed(func(prev, next int) bool { return next != prev }, filter.WithHeartbeatCount[int](3))
	values := []int{0, 0, 0, 0, 0, 1, 1, 1, 1}
	if got, want := passes(pass, values...), []bool{true, false, false, true, false, true, false, false, true}; !slices.Equal(got, want) {
		t.Errorf("Changed with WithHeartbeatCount passed %v of %v, want %v", got, values, want)
	}
}

// Of values reaching Changed at once with WithHeartbeatCount, one in n passes, as if they came one by one.
func TestChanged_WithHeartbeatCount_Concurrent(t *testing.T) {
	t.Parallel()

	pass := filter.Changed(func(int, int) bool { return false }, filter.WithHeartbeatCount[int](10))
	var (
		passed atomic.Int64
		wg     sync.WaitGroup
	)
	for v := range 100 {
		wg.Go(func() {
			if pass(v) {
				passed.Add(1)
			}
		})
	}
	wg.Wait()
	if n := passed.Load(); n != 10 {
		t.Errorf("Changed with WithHeartbeatCount(10) passed %d of 100 values, want 10", n)
	}
}

// With WithKey, each sensor counts its own values to WithHeartbeatCount.
func TestChanged_WithKeyHeartbeatCount(t *testing.T) {
	t.Parallel()

	pass := filter.Changed(func(reading, reading) bool { return false }, filter.WithKey(bySensor), filter.WithHeartbeatCount[reading](2))
	got := passes(pass, reading{"a", 0}, reading{"b", 0}, reading{"a", 0}, reading{"b", 0}, reading{"a", 0}, reading{"b", 0})
	if want := []bool{true, true, false, false, true, true}; !slices.Equal(got, want) {
		t.Errorf("Changed with WithKey and WithHeartbeatCount passed %v, want %v", got, want)
	}
}

// A WithHeartbeat shorter than Every's d stands for it.
func TestEvery_WithHeartbeat(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		pass := filter.Every(time.Hour, filter.WithHeartbeat[int](time.Second))
		got := make([]bool, 0, 4)
		for range 4 {
			got = append(got, pass(0))
			time.Sleep(500 * time.Millisecond)
		}
		if want := []bool{true, false, true, false}; !slices.Equal(got, want) {
			t.Errorf("Every with WithHeartbeat passed %v, want %v", got, want)
		}
	})
}

// With WithHeartbeatCount, Every passes the n-th value since the last one passed, and d after it, whichever comes first.
func TestEvery_WithHeartbeatCount(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		pass := filter.Every(time.Second, filter.WithHeartbeatCount[int](3))
		got := passes(pass, 0, 0, 0, 0, 0)
		time.Sleep(time.Second)
		got = append(got, passes(pass, 0, 0, 0)...)
		if want := []bool{true, false, false, true, false, true, false, false}; !slices.Equal(got, want) {
			t.Errorf("Every with WithHeartbeatCount passed %v, want %v", got, want)
		}
	})
}

// Heartbeats of 0 or less change nothing.
func TestHeartbeat_Zero(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		for _, options := range [][]filter.Option[int]{
			{filter.WithHeartbeat[int](0), filter.WithHeartbeatCount[int](0)},
			{filter.WithHeartbeat[int](-time.Second), filter.WithHeartbeatCount[int](-1)},
		} {
			changed := filter.Changed(func(int, int) bool { return false }, options...)
			every := filter.Every(time.Hour, options...)
			for i := range 3 {
				if pass := changed(0); pass != (i == 0) {
					t.Errorf("Changed with heartbeats of 0 or less passed value %d: %t", i, pass)
				}
				if pass := every(0); pass != (i == 0) {
					t.Errorf("Every with heartbeats of 0 or less passed value %d: %t", i, pass)
				}
				time.Sleep(time.Minute)
			}
		}
	})
}
