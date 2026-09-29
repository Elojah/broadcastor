package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

// Filter puts only the records keep accepts, returning Put's error, and ignores the others.
func TestFilter(t *testing.T) {
	t.Parallel()

	var got []int
	s := store.Filter(store.PutFunc[int](func(_ context.Context, r subscriber.Record[int]) error {
		got = append(got, r.Message)
		if r.Message == 3 {
			return errPut
		}

		return nil
	}), func(r subscriber.Record[int]) bool {
		return !errors.Is(r.Err, errHandle)
	})

	for msg, want := range map[int]error{1: nil, 2: nil, 3: errPut} {
		var err error
		if msg == 2 {
			err = errHandle
		}
		if got := s.Put(t.Context(), subscriber.Record[int]{Message: msg, Err: err}); !errors.Is(got, want) {
			t.Errorf("Put(%d) = %v, want %v", msg, got, want)
		}
	}
	slices.Sort(got)
	if want := []int{1, 3}; !slices.Equal(got, want) {
		t.Errorf("store got %v, want %v", got, want)
	}
}
