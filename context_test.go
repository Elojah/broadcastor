package broadcastor_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

// Once its ctx is done, a subscriber is unsubscribed: Broadcast no longer reaches it, and its goroutine ends.
func TestSubscribe_ContextDone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		id, err := b.Subscribe(ctx, handled.handle)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		b.Broadcast(t.Context(), 1)
		cancel()
		synctest.Wait()

		if n := b.Broadcast(t.Context(), 2); n != 0 {
			t.Errorf("Broadcast once ctx is done handed the message to %d subscribers, want 0", n)
		}
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once ctx is done = %v, want a *SubscriberNotFoundError", err)
		}
		synctest.Wait()
		if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

// A subscriber whose ctx is done during handle is unsubscribed right away, with its default unsubscribe options.
func TestSubscribe_ContextDoneWhileHandling(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{hold: make(chan struct{})}
		closed := &recorder[int]{}
		id, err := b.Subscribe(ctx, handled.handle, subscriber.WithBuffer[int](1),
			subscriber.WithErrorHandler[int](recordClosed(t, closed)),
			subscriber.WithUnsubscribeOptions[int](subscriber.WithUnsubscribeDiscard()))
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		// handle holds on to 1, and 2 fills the buffer.
		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2)
		cancel()
		synctest.Wait()

		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once ctx is done = %v, want a *SubscriberNotFoundError", err)
		}
		// Still subscribed, the subscriber would hold this Broadcast up until handle returns.
		if n := b.Broadcast(t.Context(), 3); n != 0 {
			t.Errorf("Broadcast once ctx is done handed the message to %d subscribers, want 0", n)
		}
		handled.release()
		synctest.Wait()

		if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
		if got, want := closed.messages(), []int{2}; !slices.Equal(got, want) {
			t.Errorf("error handler got *subscriber.ClosedError for messages %v, want %v", got, want)
		}
	})
}

// A subscriber whose ctx is already done when it subscribes is unsubscribed right after.
func TestSubscribe_ContextDoneFirst(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		if _, err := b.Subscribe(ctx, handled.handle); err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		synctest.Wait()

		if n := b.Broadcast(t.Context(), 1); n != 0 {
			t.Errorf("Broadcast handed the message to %d subscribers, want 0", n)
		}
		synctest.Wait()
		if got := handled.messages(); len(got) != 0 {
			t.Errorf("handle got %v, want nothing", got)
		}
	})
}

// A SubscribeSeq subscriber whose ctx is done before its loop starts is unsubscribed, and the loop yields nothing.
func TestSubscribeSeq_ContextDoneBeforeLoop(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		closed := &recorder[int]{}
		_, seq, err := b.SubscribeSeq(ctx, subscriber.WithBuffer[int](1),
			subscriber.WithErrorHandler[int](recordClosed(t, closed)))
		if err != nil {
			t.Fatalf("SubscribeSeq: %v", err)
		}

		b.Broadcast(t.Context(), 1) // fills the buffer
		cancel()
		synctest.Wait()

		// Still subscribed, the subscriber would hold this Broadcast up until the loop starts.
		if n := b.Broadcast(t.Context(), 2); n != 0 {
			t.Errorf("Broadcast once ctx is done handed the message to %d subscribers, want 0", n)
		}
		for msg := range seq {
			t.Errorf("loop got %d, want nothing once ctx is done", msg)
		}
		synctest.Wait()
		if got, want := closed.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("error handler got *subscriber.ClosedError for messages %v, want %v", got, want)
		}
	})
}

// A subscriber unsubscribed another way leaves nothing waiting on its ctx.
func TestSubscribe_ContextNoLeak(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		close func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID)
	}{
		{"Unsubscribe", func(t *testing.T, b *broadcastor.Broadcastor[int], id uuid.UUID) {
			t.Helper()
			unsubscribe(t, b, id)
		}},
		{"Close", func(t *testing.T, b *broadcastor.Broadcastor[int], _ uuid.UUID) {
			t.Helper()
			if err := b.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				ctx := neverDone{Context: context.Background(), done: make(chan struct{})}
				b := broadcastor.NewBroadcastor[int]()
				handled := &recorder[int]{}
				id, err := b.Subscribe(ctx, handled.handle)
				if err != nil {
					t.Fatalf("Subscribe: %v", err)
				}

				b.Broadcast(t.Context(), 1)
				tt.close(t, b, id)
				synctest.Wait()

				if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
					t.Errorf("handle got %v, want %v", got, want)
				}
			})
		})
	}
}

// Its ctx being done and Unsubscribe racing on one subscriber unsubscribe it once between them.
func TestSubscribe_ContextDoneRacesUnsubscribe(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		id, err := b.Subscribe(ctx, handled.handle)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		go cancel()
		if err := b.Unsubscribe(t.Context(), id); err != nil && !isNotFound(err) {
			t.Errorf("Unsubscribe = %v, want nil or a *SubscriberNotFoundError", err)
		}
		synctest.Wait()

		if n := b.Broadcast(t.Context(), 1); n != 0 {
			t.Errorf("Broadcast handed the message to %d subscribers, want 0", n)
		}
	})
}

// A subscriber with subscriber.WithDetachedContext stays subscribed once its ctx is done, and handle gets a ctx with
// the same values that is never done.
func TestSubscriberWithDetachedContext(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, "subscribe"))
		b := broadcastor.NewBroadcastor[int]()
		handled := &recorder[int]{}
		id, err := b.Subscribe(ctx, func(ctx context.Context, id uuid.UUID, msg int) error {
			if err := ctx.Err(); err != nil {
				t.Errorf("handle got a done ctx: %v", err)
			}
			if got, want := ctx.Value(key{}), "subscribe"; got != want {
				t.Errorf("handle got ctx value %v, want %v", got, want)
			}

			return handled.handle(ctx, id, msg)
		}, subscriber.WithDetachedContext[int]())
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		cancel()
		synctest.Wait()

		if n := b.Broadcast(t.Context(), 1); n != 1 {
			t.Errorf("Broadcast once ctx is done handed the message to %d subscribers, want 1", n)
		}
		unsubscribe(t, b, id)
		synctest.Wait()
		if got, want := handled.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("handle got %v, want %v", got, want)
		}
	})
}

// A SubscribeSeq loop with subscriber.WithDetachedContext goes on once its ctx is done, and ends once its subscriber is
// unsubscribed.
func TestSubscriberWithDetachedContext_SubscribeSeq(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		b := broadcastor.NewBroadcastor[int]()
		id, seq, err := b.SubscribeSeq(ctx, subscriber.WithDetachedContext[int]())
		if err != nil {
			t.Fatalf("SubscribeSeq: %v", err)
		}

		got := &recorder[int]{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for msg := range seq {
				got.record(msg)
			}
		}()
		cancel()
		synctest.Wait()

		if n := b.Broadcast(t.Context(), 1); n != 1 {
			t.Errorf("Broadcast once ctx is done handed the message to %d subscribers, want 1", n)
		}
		unsubscribe(t, b, id)
		waitClosed(t, done, "the loop to end once its subscriber is unsubscribed")
		if got, want := got.messages(), []int{1}; !slices.Equal(got, want) {
			t.Errorf("loop got %v, want %v", got, want)
		}
	})
}

// message.WithContext replaces the Subscribe ctx for one message, and a Broadcast overrides a default one, nil
// included.
func TestMessageWithContext(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		ctx := context.WithValue(subscribeCtx(t), key{}, "subscribe")
		messageCtx := context.WithValue(t.Context(), key{}, "message")
		defaultCtx := context.WithValue(t.Context(), key{}, "default")
		b := broadcastor.NewBroadcastor[int]()
		subscribeRecording := func(options ...subscriber.Option[int]) (*recorder[string], uuid.UUID) {
			values := &recorder[string]{}
			id, err := b.Subscribe(ctx, func(ctx context.Context, _ uuid.UUID, _ int) error {
				v, _ := ctx.Value(key{}).(string)
				values.record(v)

				return nil
			}, options...)
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}

			return values, id
		}
		plain, plainID := subscribeRecording()
		defaulted, defaultedID := subscribeRecording(
			subscriber.WithDefaultMessageOptions(message.WithContext[int](defaultCtx)))

		b.Broadcast(t.Context(), 1)
		b.Broadcast(t.Context(), 2, message.WithContext[int](messageCtx))
		b.Broadcast(t.Context(), 3, message.WithContext[int](nil)) //nolint:staticcheck // nil is how to override a default
		unsubscribeAll(t, b, plainID, defaultedID)
		synctest.Wait()

		if got, want := plain.messages(), []string{"subscribe", "message", "subscribe"}; !slices.Equal(got, want) {
			t.Errorf("handle got ctx values %v, want %v", got, want)
		}
		if got, want := defaulted.messages(), []string{"default", "message", "subscribe"}; !slices.Equal(got, want) {
			t.Errorf("handle with a default message ctx got ctx values %v, want %v", got, want)
		}
	})
}

// A message's ctx reaches handle as is, done or not, and the Subscribe ctx being done still unsubscribes.
func TestMessageWithContext_Cancel(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(subscribeCtx(t))
		b := broadcastor.NewBroadcastor[int]()
		release := make(chan struct{})
		errs := &recorder[error]{}
		id, err := b.Subscribe(ctx, func(ctx context.Context, _ uuid.UUID, _ int) error {
			<-release
			errs.record(ctx.Err())

			return nil
		}, subscriber.WithBuffer[int](2))
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		// handle holds on to 0, and 1 and 2 fill the buffer.
		requestCtx, cancelRequest := context.WithCancel(t.Context())
		b.Broadcast(t.Context(), 0)
		b.Broadcast(t.Context(), 1, message.WithContext[int](requestCtx))
		b.Broadcast(t.Context(), 2, message.WithContext[int](context.WithoutCancel(requestCtx)))
		cancelRequest()
		cancel()
		synctest.Wait()
		if err := b.Unsubscribe(t.Context(), id); !isNotFound(err) {
			t.Errorf("Unsubscribe once ctx is done = %v, want a *SubscriberNotFoundError", err)
		}
		close(release)
		synctest.Wait()

		if got, want := errs.messages(), []error{context.Canceled, context.Canceled, nil}; !slices.Equal(got, want) {
			t.Errorf("handle got ctx errors %v, want %v", got, want)
		}
	})
}

// Every error about a message with its own ctx reaches both error handlers with that ctx, whatever the error.
func TestMessageWithContext_ErrorHandlers(t *testing.T) {
	t.Parallel()

	type key struct{}
	for _, tt := range []struct {
		name string
		want error
		// fail makes a subscriber fail on message 1, sent with options, which give it its ctx and error handler.
		fail func(t *testing.T, b *broadcastor.Broadcastor[int], errorHandler subscriber.Option[int], options ...message.Option[int])
	}{
		{"handle", handleError(1), func(t *testing.T, b *broadcastor.Broadcastor[int], errorHandler subscriber.Option[int], options ...message.Option[int]) {
			t.Helper()
			id := subscribe(t, b, func(_ context.Context, _ uuid.UUID, msg int) error {
				return handleError(msg)
			}, errorHandler)
			b.Broadcast(t.Context(), 1, options...)
			unsubscribe(t, b, id)
		}},
		{"Timeout", subscriber.ErrTimeout, func(t *testing.T, b *broadcastor.Broadcastor[int], errorHandler subscriber.Option[int], options ...message.Option[int]) {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, b, stuck.handle, errorHandler)
			b.Broadcast(t.Context(), 0) // stuck is now processing 0 and not reading
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			b.Broadcast(ctx, 1, options...)
			unsubscribe(t, b, id)
			stuck.release()
		}},
		{"Dropped", subscriber.ErrDropped, func(t *testing.T, b *broadcastor.Broadcastor[int], errorHandler subscriber.Option[int], options ...message.Option[int]) {
			t.Helper()
			stuck := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, b, stuck.handle, errorHandler,
				subscriber.WithDefaultMessageOptions(message.WithNonBlocking[int]()))
			b.Broadcast(t.Context(), 0, message.WithSync[int]()) // stuck is now processing 0 and not reading
			b.Broadcast(t.Context(), 1, options...)
			unsubscribe(t, b, id)
			stuck.release()
		}},
		{"SubscribeSeq", subscriber.ErrClosed, func(t *testing.T, b *broadcastor.Broadcastor[int], errorHandler subscriber.Option[int], options ...message.Option[int]) {
			t.Helper()
			_, seq := subscribeSeq(t, b, subscriber.WithBuffer[int](2), errorHandler)
			b.Broadcast(t.Context(), 0)
			b.Broadcast(t.Context(), 1, options...)
			for range seq {
				break // after 0, leaving 1 unyielded
			}
		}},
		{"WithUnsubscribeDiscard", subscriber.ErrClosed, func(t *testing.T, b *broadcastor.Broadcastor[int], errorHandler subscriber.Option[int], options ...message.Option[int]) {
			t.Helper()
			held := &recorder[int]{hold: make(chan struct{})}
			id := subscribe(t, b, held.handle, subscriber.WithBuffer[int](1), errorHandler)
			b.Broadcast(t.Context(), 0) // held holds on to 0, and 1 fills the buffer
			b.Broadcast(t.Context(), 1, options...)
			if err := b.Unsubscribe(t.Context(), id, subscriber.WithUnsubscribeDiscard()); err != nil {
				t.Fatalf("Unsubscribe: %v", err)
			}
			held.release()
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				got := &recorder[string]{}
				errorHandler := func(who string) func(context.Context, error) {
					return func(ctx context.Context, err error) {
						v, _ := ctx.Value(key{}).(string)
						got.record(fmt.Sprintf("%s: %t, ctx %s, done %v", who, errors.Is(err, tt.want), v, ctx.Err()))
					}
				}
				b := broadcastor.NewBroadcastor[int]()
				tt.fail(t, b, subscriber.WithErrorHandler[int](errorHandler("subscriber")),
					message.WithContext[int](context.WithValue(t.Context(), key{}, "message")),
					message.WithErrorHandler[int](errorHandler("message")))
				synctest.Wait()

				want := []string{"subscriber: true, ctx message, done <nil>", "message: true, ctx message, done <nil>"}
				if got := got.messages(); !slices.Equal(got, want) {
					t.Errorf("error handlers got %q, want %q", got, want)
				}
			})
		})
	}
}

// neverDone is a ctx type the context package does not know, so AfterFunc on it starts a goroutine, which leaks unless
// stopped.
type neverDone struct {
	context.Context //nolint:containedctx // it is the ctx itself, not a struct carrying one

	done chan struct{}
}

func (c neverDone) Done() <-chan struct{} {
	return c.done
}
