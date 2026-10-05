package filter_test

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elojah/broadcastor/filter"
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
