package limit_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elojah/broadcastor/pkg/limit"
)

// Acquire takes at most n slots, and waits for one to be released after that.
func TestSemaphore_Acquire(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		s := limit.NewSemaphore(2)
		for range 2 {
			if err := s.Acquire(t.Context()); err != nil {
				t.Fatalf("Acquire = %v with a free slot, want nil", err)
			}
		}

		acquired := make(chan error)
		go func() { acquired <- s.Acquire(t.Context()) }()
		synctest.Wait()
		select {
		case err := <-acquired:
			t.Fatalf("Acquire returned %v with no free slot, want it to wait", err)
		default:
		}

		s.Release()
		if err := <-acquired; err != nil {
			t.Errorf("Acquire = %v once a slot was released, want nil", err)
		}
	})
}

// Acquire gives up once ctx is done, without taking a slot.
func TestSemaphore_AcquireContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		s := limit.NewSemaphore(1)
		if err := s.Acquire(t.Context()); err != nil {
			t.Fatalf("Acquire = %v, want nil", err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		if err := s.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Acquire = %v with no free slot, want %v", err, context.DeadlineExceeded)
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("Acquire gave up after %v, want %v", elapsed, time.Second)
		}

		// The Acquire that gave up took no slot, so one Release frees the only one.
		s.Release()
		if err := s.Acquire(t.Context()); err != nil {
			t.Errorf("Acquire = %v once the slot was released, want nil", err)
		}
	})
}

// A free slot wins over a ctx that is already done.
func TestSemaphore_AcquireContextDoneSlotFree(t *testing.T) {
	t.Parallel()

	s := limit.NewSemaphore(1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Acquire(ctx); err != nil {
		t.Errorf("Acquire = %v with a free slot, want nil", err)
	}
}
