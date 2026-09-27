package broadcastor_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elojah/broadcastor"
)

type (
	subscribeKey struct{}
	broadcastKey struct{}
	sharedKey    struct{}
)

var (
	errBroadcastDone = errors.New("broadcast done")
	errSubscribeDone = errors.New("subscribe done")
)

// With WithSubscriberBroadcastValues, handle and its error handler get the values of the Broadcast ctx of each message,
// even once several are waiting in the buffer, on top of those of the Subscribe ctx. Without it, they get the Subscribe
// ctx alone.
func TestWithSubscriberBroadcastValues(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx := context.WithValue(context.WithValue(t.Context(), subscribeKey{}, "s"), sharedKey{}, "s")
		b := broadcastor.NewBroadcastor[int]()
		hold := make(chan struct{})
		withValues := describing(t, ctx, b, hold, broadcastor.WithSubscriberBroadcastValues[int]())
		without := describing(t, ctx, b, hold)

		// Both subscribers hold on to 1, and 2 and 3 fill their buffers.
		for msg := 1; msg <= 3; msg++ {
			b.Broadcast(context.WithValue(context.WithValue(t.Context(), broadcastKey{}, msg), sharedKey{}, fmt.Sprint("b", msg)), msg)
		}
		close(hold)
		synctest.Wait()

		want := []string{"subscribe=s broadcast=1 shared=b1", "subscribe=s broadcast=2 shared=b2", "subscribe=s broadcast=3 shared=b3"}
		if got := withValues.handled.messages(); !slices.Equal(got, want) {
			t.Errorf("with WithSubscriberBroadcastValues, handle got a ctx with %q, want %q", got, want)
		}
		if got := withValues.reported.messages(); !slices.Equal(got, want) {
			t.Errorf("with WithSubscriberBroadcastValues, the error handler got a ctx with %q, want %q", got, want)
		}
		want = slices.Repeat([]string{"subscribe=s broadcast=<nil> shared=s"}, 3)
		if got := without.handled.messages(); !slices.Equal(got, want) {
			t.Errorf("without WithSubscriberBroadcastValues, handle got a ctx with %q, want %q", got, want)
		}
		if got := without.reported.messages(); !slices.Equal(got, want) {
			t.Errorf("without WithSubscriberBroadcastValues, the error handler got a ctx with %q, want %q", got, want)
		}

		if err := b.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}

// The ctx handle gets with WithSubscriberBroadcastValues has the deadline and cancellation of the Subscribe ctx, not
// those of the Broadcast ctx, which is done and past its deadline by the time handle runs. context.Cause and the ctxs
// derived from it follow the Subscribe ctx too.
func TestWithSubscriberBroadcastValues_Cancellation(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		hold := make(chan struct{})
		ctxs := make(chan context.Context, 2)
		id, err := b.Subscribe(ctx, func(ctx context.Context, msg int) error {
			ctxs <- ctx
			if msg == 1 {
				<-hold
			}

			return nil
		}, broadcastor.WithSubscriberBroadcastValues[int](), broadcastor.WithSubscriberBuffer[int](1))
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		// handle holds on to 1, and 2 waits in the buffer while the Broadcast ctx is cancelled and past its deadline.
		bctx, cancelBroadcast := context.WithCancelCause(context.WithValue(t.Context(), broadcastKey{}, "b"))
		bctx, cancelDeadline := context.WithTimeout(bctx, time.Second)
		defer cancelDeadline()
		b.Broadcast(bctx, 1)
		b.Broadcast(bctx, 2)
		cancelBroadcast(errBroadcastDone)
		time.Sleep(2 * time.Second)
		close(hold)
		synctest.Wait()

		first, second := <-ctxs, <-ctxs
		for i, got := range []context.Context{first, second} {
			if v := got.Value(broadcastKey{}); v != "b" {
				t.Errorf("message %d: handle got a ctx with value %v, want the Broadcast ctx's", i+1, v)
			}
			if err := got.Err(); err != nil {
				t.Errorf("message %d: handle got a ctx done with %v once the Broadcast ctx was, want it not done", i+1, err)
			}
			if cause := context.Cause(got); cause != nil {
				t.Errorf("message %d: context.Cause of handle's ctx = %v, want nil", i+1, cause)
			}
			if deadline, ok := got.Deadline(); ok {
				t.Errorf("message %d: handle got a ctx with the Broadcast ctx's deadline %v, want none", i+1, deadline)
			}
		}

		derived, cancelDerived := context.WithCancel(second)
		defer cancelDerived()
		cancel(errSubscribeDone)
		synctest.Wait()
		for what, got := range map[string]context.Context{"handle's ctx": second, "a ctx derived from handle's": derived} {
			if err := got.Err(); !errors.Is(err, context.Canceled) {
				t.Errorf("once the Subscribe ctx is cancelled, %s is done with %v, want %v", what, err, context.Canceled)
			}
			if cause := context.Cause(got); !errors.Is(cause, errSubscribeDone) {
				t.Errorf("once the Subscribe ctx is cancelled, context.Cause of %s = %v, want %v", what, cause, errSubscribeDone)
			}
		}

		unsubscribe(t, b, id)
	})
}

// A SubscribeSeq loop body is given no ctx, but the messages the loop took and never yielded are reported with the
// values of their own Broadcast ctx, on top of those of the SubscribeSeq ctx.
func TestWithSubscriberBroadcastValues_SubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx := context.WithValue(context.WithValue(t.Context(), subscribeKey{}, "s"), sharedKey{}, "s")
		b := broadcastor.NewBroadcastor[int]()
		reported := &recorder[string]{}
		_, seq, err := b.SubscribeSeq(ctx, broadcastor.WithSubscriberBroadcastValues[int](), broadcastor.WithSubscriberBuffer[int](2),
			broadcastor.WithSubscriberErrorHandler[int](func(ctx context.Context, err error) {
				if !errors.Is(err, broadcastor.ErrSubscriberClosed) {
					t.Errorf("error handler got %v, want a *SubscriberClosedError", err)
				}
				reported.record(describe(ctx))
			}))
		if err != nil {
			t.Fatalf("SubscribeSeq: %v", err)
		}

		for msg := 1; msg <= 2; msg++ {
			b.Broadcast(context.WithValue(context.WithValue(t.Context(), broadcastKey{}, msg), sharedKey{}, fmt.Sprint("b", msg)), msg)
		}
		for range seq {
			break
		}
		synctest.Wait()

		if got, want := reported.messages(), []string{"subscribe=s broadcast=2 shared=b2"}; !slices.Equal(got, want) {
			t.Errorf("error handler got a ctx with %q, want %q", got, want)
		}
	})
}

// described records the values of every ctx a subscriber's handle and error handler are given, as describe does.
type described struct {
	handled  *recorder[string]
	reported *recorder[string]
}

// describing subscribes to b with ctx and a buffer of 2. Its handle holds on to the first message until hold is closed,
// and fails on every message.
func describing(
	t *testing.T, ctx context.Context, b *broadcastor.Broadcastor[int], hold <-chan struct{},
	options ...broadcastor.SubscriberOption[int],
) described {
	t.Helper()
	d := described{handled: &recorder[string]{}, reported: &recorder[string]{}}
	options = append(options, broadcastor.WithSubscriberBuffer[int](2),
		broadcastor.WithSubscriberErrorHandler[int](func(ctx context.Context, _ error) {
			d.reported.record(describe(ctx))
		}))
	_, err := b.Subscribe(ctx, func(ctx context.Context, msg int) error {
		d.handled.record(describe(ctx))
		<-hold

		return handleError(msg)
	}, options...)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	return d
}

// describe returns the values of ctx under the keys these tests set.
func describe(ctx context.Context) string {
	return fmt.Sprintf("subscribe=%v broadcast=%v shared=%v", ctx.Value(subscribeKey{}), ctx.Value(broadcastKey{}), ctx.Value(sharedKey{}))
}
