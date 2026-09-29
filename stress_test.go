package broadcastor_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"math"
	"math/rand/v2"
	"runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
)

const (
	stressRounds       = 10
	stressBroadcasters = 4
	stressChurners     = 4
	// stressOps is how many Broadcasts each broadcaster makes, and how many subscriptions each churner starts or ends,
	// per round.
	stressOps = 200
	// stressLive bounds each churner's live subscriptions.
	stressLive = 8
	// stressTimeout bounds the timeouts and the pauses in handle, so that some messages time out or are dropped.
	stressTimeout = 500 * time.Microsecond
	// handle fails for every multiple of stressFailEvery.
	stressFailEvery = 7
	// stressLabel is the pprof label every goroutine of a round carries, the library's included.
	stressLabel = "broadcastor_stress"
)

// TestStress runs random interleavings of every method from several goroutines, in real time so that timeouts race
// sends, over rounds that each have their own Broadcastor. It checks that nothing panics, that every goroutine returns
// once the Broadcastor is closed, that no subscriber gets a message twice, whether handled or reported, that a
// subscriber gets every message broadcast while it was subscribed and none broadcast outside that, and that Stats add
// up once nothing is in flight. make stress runs it many times.
func TestStress(t *testing.T) {
	t.Parallel()

	for round := range stressRounds {
		// Half the rounds close while everything else runs, the others once nothing is in flight, after checking Stats.
		closeEarly := round%2 == 1
		runStress(t, closeEarly)
		if t.Failed() {
			t.Fatalf("round %d failed, closing early: %t", round, closeEarly)
		}
	}
}

// runStress runs one round, then checks it once every goroutine it started has returned.
func runStress(t *testing.T, closeEarly bool) {
	t.Helper()
	s := &stress{b: broadcastor.NewBroadcastor[int](), closeEarly: closeEarly, label: uuid.NewString()}
	// Every goroutine started in f carries the label, and so do those they start, the library's included.
	pprof.Do(t.Context(), pprof.Labels(stressLabel, s.label), func(ctx context.Context) { s.run(ctx, t) })
	s.waitGoroutines(t)
	// Only now, so that cancelling cannot end a SubscribeSeq loop that Close failed to end.
	for _, sub := range s.subs {
		sub.cancel()
	}
	s.check(t)
}

// stress is one round of TestStress.
type stress struct {
	b          *broadcastor.Broadcastor[int]
	closeEarly bool
	// label is the value of stressLabel for the round's goroutines.
	label string

	// clock orders what the goroutines do: its atomics are sequentially consistent, so a tick taken after another
	// happened after it.
	clock atomic.Int64
	// next is the last number broadcast.
	next atomic.Int64
	// closing and closed are the ticks before the first Close and after it returned, 0 until then.
	closing, closed atomic.Int64

	// loops are the SubscribeSeq loops.
	loops sync.WaitGroup

	mu         sync.Mutex
	broadcasts []stressBroadcast
	subs       []*stressSub
}

// stressBroadcast is the Broadcast of number, between the ticks start and end.
type stressBroadcast struct {
	number     int
	start, end int64
}

// stressSub is a subscription, with everything its handle or loop body and its error handler got.
type stressSub struct {
	id     uuid.UUID
	seq    bool
	cancel context.CancelFunc

	// subscribing and subscribed are the ticks before Subscribe or SubscribeSeq and after it returned.
	subscribing, subscribed int64
	// leaving is the earliest tick before something removed the subscriber, and gone the earliest after an Unsubscribe
	// that did. 0 until then.
	leaving, gone atomic.Int64
	// leave makes handle unsubscribe itself, or the loop body break, on the next message.
	leave atomic.Bool

	mu     sync.Mutex
	got    map[int]stressOutcome
	failed map[int]bool
	// twice holds the numbers the subscriber got again, or that handle failed for again.
	twice      []int
	unexpected []error
}

// stressOutcome is what became of a message a subscriber got.
type stressOutcome int

const (
	stressHandled stressOutcome = iota
	stressClosed
	stressTimedOut
	stressDropped
)

// run starts the broadcasters and churners, checks Stats once they are done unless it closed early, then closes.
func (s *stress) run(ctx context.Context, t *testing.T) {
	t.Helper()
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range stressBroadcasters {
		workers.Go(func() {
			<-start
			s.broadcaster(ctx, t)
		})
	}
	for i := range stressChurners {
		closeAt := -1
		if i == 0 && s.closeEarly {
			closeAt = randN(stressOps)
		}
		workers.Go(func() {
			<-start
			s.churn(ctx, t, closeAt)
		})
	}
	close(start)
	s.wait(t, &workers, "broadcasters and churners")

	if !s.closeEarly {
		s.checkStats(t)
	}
	switch err := s.close(); {
	case s.closeEarly && !errors.Is(err, broadcastor.ErrClosed):
		t.Errorf("second Close = %v, want ErrClosed", err)
	case !s.closeEarly && err != nil:
		t.Errorf("Close = %v, want nil", err)
	}
	s.wait(t, &s.loops, "SubscribeSeq loops")
	if stats := s.b.Stats(); len(stats) != 0 {
		t.Errorf("Stats after Close are for %d subscribers, want none", len(stats))
	}
}

// broadcaster broadcasts stressOps unique numbers, each with a random delivery and timeout.
func (s *stress) broadcaster(ctx context.Context, t *testing.T) {
	t.Helper()
	broadcasts := make([]stressBroadcast, 0, stressOps)
	for range stressOps {
		n := int(s.next.Add(1))
		broadcastCtx, cancel, options := stressMessage(ctx)
		start := s.clock.Add(1)
		p := recovered(func() { s.b.Broadcast(broadcastCtx, n, options...) })
		end := s.clock.Add(1)
		cancel()
		if p != nil {
			t.Errorf("Broadcast(%d) panicked: %v", n, p)
		}
		broadcasts = append(broadcasts, stressBroadcast{number: n, start: start, end: end})
		// So that the workers take turns with GOMAXPROCS 1 too, instead of one running all its ops at once.
		runtime.Gosched()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcasts = append(s.broadcasts, broadcasts...)
}

// churn starts and ends subscriptions stressOps times, keeping at most stressLive at once, and closes the Broadcastor
// at op closeAt, if it is one.
func (s *stress) churn(ctx context.Context, t *testing.T, closeAt int) {
	t.Helper()
	var live []*stressSub
	for op := range stressOps {
		switch {
		case op == closeAt:
			if err := s.close(); err != nil {
				t.Errorf("Close = %v, want nil", err)
			}
		case len(live) == 0 || (len(live) < stressLive && randN(2) == 0):
			if sub := s.subscribe(ctx, t); sub != nil {
				live = append(live, sub)
			}
		default:
			i := randN(len(live))
			s.end(ctx, t, live[i])
			live = slices.Delete(live, i, i+1)
		}
		runtime.Gosched()
	}
}

// subscribe subscribes with Subscribe or SubscribeSeq and random options. It returns nil once the Broadcastor is
// closed.
func (s *stress) subscribe(ctx context.Context, t *testing.T) *stressSub {
	t.Helper()
	// Done only through cancel, like subscribeCtx, so that a subscriber left over fails the round.
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	sub := &stressSub{seq: randN(3) == 0, cancel: cancel, got: map[int]stressOutcome{}, failed: map[int]bool{}}
	options := []subscriber.Option[int]{
		subscriber.WithBuffer[int](randN(4)),
		subscriber.WithErrorHandler[int](sub.report),
	}
	// So that ctx and Close discard too.
	if randN(2) == 0 {
		options = append(options, subscriber.WithUnsubscribeOptions[int](subscriber.WithUnsubscribeDiscard()))
	}

	var (
		seq iter.Seq[int]
		err error
	)
	sub.subscribing = s.clock.Add(1)
	if sub.seq {
		sub.id, seq, err = s.b.SubscribeSeq(ctx, options...)
	} else {
		sub.id, err = s.b.Subscribe(ctx, s.handle(sub), options...)
	}
	sub.subscribed = s.clock.Add(1)
	if err != nil {
		cancel()
		// Close takes its tick before closing, so a Subscribe it refused sees it.
		if !errors.Is(err, broadcastor.ErrClosed) || s.closing.Load() == 0 {
			t.Errorf("subscribing = %v, want nil, or ErrClosed once closed", err)
		}

		return nil
	}
	if sub.seq {
		s.loops.Go(func() { s.loop(sub, seq) })
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = append(s.subs, sub)

	return sub
}

// end ends sub with Unsubscribe, with or without discard, by cancelling its ctx, or from its own handle or loop body.
func (s *stress) end(ctx context.Context, t *testing.T, sub *stressSub) {
	t.Helper()
	var options []subscriber.UnsubscribeOption
	switch randN(5) {
	case 0:
		options = append(options, subscriber.WithUnsubscribeDiscard())
	case 1:
		options = append(options, subscriber.WithUnsubscribeDeliver())
	case 2:
		earliest(&sub.leaving, s.clock.Add(1))
		sub.cancel()

		return
	case 3:
		// handle or the loop body records leaving itself, on the next message.
		sub.leave.Store(true)

		return
	}
	if err := s.unsubscribe(ctx, sub, sub.id, options...); err != nil {
		t.Errorf("Unsubscribe(%s) = %v, want nil, or *SubscriberNotFoundError once closed", sub.id, err)
	}
}

// close closes the Broadcastor, and records the ticks around the first Close.
func (s *stress) close() error {
	s.closing.CompareAndSwap(0, s.clock.Add(1))
	err := s.b.Close()
	s.closed.CompareAndSwap(0, s.clock.Add(1))

	return err
}

// unsubscribe unsubscribes sub, whose ID is id, and records the ticks around it. Once closed, sub may be gone already.
func (s *stress) unsubscribe(ctx context.Context, sub *stressSub, id uuid.UUID, options ...subscriber.UnsubscribeOption) error {
	earliest(&sub.leaving, s.clock.Add(1))
	err := s.b.Unsubscribe(ctx, id, options...)
	switch {
	case err == nil:
		earliest(&sub.gone, s.clock.Add(1))

		return nil
	case isNotFound(err) && s.closing.Load() != 0:
		return nil
	default:
		return err
	}
}

// handle records each message as handled, pauses now and then, unsubscribes itself once sub.leave is set, and fails
// for every multiple of stressFailEvery. It takes the ID from its arguments, since sub.id is set once Subscribe returns.
func (s *stress) handle(sub *stressSub) func(context.Context, uuid.UUID, int) error {
	return func(ctx context.Context, id uuid.UUID, n int) error {
		sub.record(n, stressHandled)
		stressPause()
		if sub.leave.CompareAndSwap(true, false) {
			var options []subscriber.UnsubscribeOption
			if randN(2) == 0 {
				options = append(options, subscriber.WithUnsubscribeDiscard())
			}
			if err := s.unsubscribe(ctx, sub, id, options...); err != nil {
				sub.unexpect(err)
			}
		}
		if n%stressFailEvery == 0 {
			return handleError(n)
		}

		return nil
	}
}

// loop ranges over seq, recording each message as handled and pausing now and then, until sub.leave is set.
func (s *stress) loop(sub *stressSub, seq iter.Seq[int]) {
	for n := range seq {
		sub.record(n, stressHandled)
		stressPause()
		if sub.leave.Load() {
			earliest(&sub.leaving, s.clock.Add(1))

			break
		}
	}
}

// checkStats waits until the Stats of every subscriber still subscribed match what it got, which they do once nothing
// is in flight, or fails after deadlockTimeout.
func (s *stress) checkStats(t *testing.T) {
	t.Helper()
	subs := make(map[uuid.UUID]*stressSub, len(s.subs))
	for _, sub := range s.subs {
		subs[sub.id] = sub
	}

	deadline := time.Now().Add(deadlockTimeout)
	for {
		mismatch := statsMismatch(s.b.Stats(), subs)
		if mismatch == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("Stats still do not add up after %v: %s", deadlockTimeout, mismatch)

			return
		}
		time.Sleep(time.Millisecond)
	}
}

// check checks what each subscriber got, and reports the first few subscribers that got something wrong.
func (s *stress) check(t *testing.T) {
	t.Helper()
	const reported = 5
	byNumber := make(map[int]stressBroadcast, len(s.broadcasts))
	for _, b := range s.broadcasts {
		byNumber[b.number] = b
	}
	// Every round closes, so both are set.
	closing, closed := s.closing.Load(), s.closed.Load()

	failing := 0
	for i, sub := range s.subs {
		problems := sub.problems(s.broadcasts, byNumber, closing, closed)
		if len(problems) == 0 {
			continue
		}
		if failing++; failing <= reported {
			t.Errorf("subscriber %d (%s, SubscribeSeq: %t, ticks: subscribing %d, subscribed %d, leaving %d, gone %d):\n%s",
				i, sub.id, sub.seq, sub.subscribing, sub.subscribed, sub.leaving.Load(), sub.gone.Load(),
				strings.Join(problems, "\n"))
		}
	}
	if failing > reported {
		t.Errorf("and %d more subscribers", failing-reported)
	}
}

// wait waits for wg, or fails with the stacks of the round's goroutines after deadlockTimeout.
func (s *stress) wait(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deadlockTimeout):
		t.Fatalf("still waiting for %s after %v: deadlock. The round's goroutines:\n%s", what, deadlockTimeout, strings.Join(s.goroutines(t), "\n\n"))
	}
}

// waitGoroutines waits until none of the round's goroutines is left, or fails with their stacks after
// deadlockTimeout. It must not be called from one of them.
func (s *stress) waitGoroutines(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(deadlockTimeout)
	for {
		left := s.goroutines(t)
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines still running %v after Close:\n%s", deadlockTimeout, strings.Join(left, "\n\n"))
		}
		time.Sleep(time.Millisecond)
	}
}

// goroutines returns the goroutine profile records, as pprof prints them with their stacks, of the round's goroutines.
func (s *stress) goroutines(t *testing.T) []string {
	t.Helper()
	var profile bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&profile, 1); err != nil {
		t.Fatalf("goroutine profile: %v", err)
	}
	label := fmt.Sprintf("%q:%q", stressLabel, s.label)
	var records []string
	for record := range strings.SplitSeq(profile.String(), "\n\n") {
		if strings.Contains(record, "# labels: ") && strings.Contains(record, label) {
			records = append(records, record)
		}
	}

	return records
}

// record records that the subscriber got n, handled or reported as outcome.
func (sub *stressSub) record(n int, outcome stressOutcome) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if _, ok := sub.got[n]; ok {
		sub.twice = append(sub.twice, n)

		return
	}
	sub.got[n] = outcome
}

// report is the subscriber's error handler.
func (sub *stressSub) report(_ context.Context, err error) {
	var (
		failed  handleError
		closed  *subscriber.ClosedError[int]
		timeout *subscriber.TimeoutError[int]
		dropped *subscriber.DroppedError[int]
	)
	switch {
	case errors.As(err, &failed):
		sub.fail(int(failed))
	case errors.As(err, &closed):
		sub.record(closed.Message, stressClosed)
	case errors.As(err, &timeout):
		sub.record(timeout.Message, stressTimedOut)
	case errors.As(err, &dropped):
		sub.record(dropped.Message, stressDropped)
	default:
		sub.unexpect(err)
	}
}

// fail records that handle failed for n.
func (sub *stressSub) fail(n int) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.failed[n] {
		sub.twice = append(sub.twice, n)

		return
	}
	sub.failed[n] = true
}

// unexpect records an error the subscriber should not have got.
func (sub *stressSub) unexpect(err error) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.unexpected = append(sub.unexpected, err)
}

// problems checks what the subscriber got against broadcasts, which byNumber indexes, given the ticks around the
// first Close.
func (sub *stressSub) problems(broadcasts []stressBroadcast, byNumber map[int]stressBroadcast, closing, closed int64) []string {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	var problems []string
	if len(sub.twice) != 0 {
		problems = append(problems, fmt.Sprintf("got %v twice", first(sub.twice)))
	}
	if len(sub.unexpected) != 0 {
		problems = append(problems, fmt.Sprintf("got unexpected errors %v", first(sub.unexpected)))
	}

	// Every message broadcast entirely while it was subscribed, and none broadcast entirely outside that.
	leaving, gone := min(tickOrNever(&sub.leaving), closing), min(tickOrNever(&sub.gone), closed)
	var missing, extra []int
	for _, b := range broadcasts {
		if _, ok := sub.got[b.number]; !ok && b.start > sub.subscribed && b.end < leaving {
			missing = append(missing, b.number)
		}
	}
	for n := range sub.got {
		if b, ok := byNumber[n]; !ok || b.end < sub.subscribing || b.start > gone {
			extra = append(extra, n)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		problems = append(problems, fmt.Sprintf("missed %d messages broadcast while it was subscribed: %v, the first %+v",
			len(missing), first(missing), byNumber[missing[0]]))
	}
	if len(extra) != 0 {
		slices.Sort(extra)
		problems = append(problems, fmt.Sprintf("got %d messages broadcast before it subscribed or after it was unsubscribed: %v, the first %+v",
			len(extra), first(extra), byNumber[extra[0]]))
	}

	// handle failed for every multiple of stressFailEvery it got, and a loop body never fails.
	var wrongFailures []int
	for n, outcome := range sub.got {
		if fails := !sub.seq && outcome == stressHandled && n%stressFailEvery == 0; fails != sub.failed[n] {
			wrongFailures = append(wrongFailures, n)
		}
	}
	for n := range sub.failed {
		if outcome, ok := sub.got[n]; !ok || outcome != stressHandled {
			wrongFailures = append(wrongFailures, n)
		}
	}
	if len(wrongFailures) != 0 {
		slices.Sort(wrongFailures)
		problems = append(problems, fmt.Sprintf("the error handler got a wrong set of handle's failures, with %v wrong", first(wrongFailures)))
	}

	return problems
}

// statsMismatch returns how the Stats of a subscriber differ from what it got, or "" if none do.
func statsMismatch(stats []subscriber.Stats, subs map[uuid.UUID]*stressSub) string {
	for _, got := range stats {
		sub, ok := subs[got.SubscriberID]
		if !ok {
			return fmt.Sprintf("Stats for subscriber %s, which never subscribed", got.SubscriberID)
		}

		sub.mu.Lock()
		counts := make(map[stressOutcome]uint64, len(sub.got))
		for _, outcome := range sub.got {
			counts[outcome]++
		}
		failed := uint64(len(sub.failed))
		sub.mu.Unlock()

		// Nothing queued, and HandleTime cannot be known.
		want := subscriber.Stats{
			SubscriberID: got.SubscriberID,
			Buffer:       got.Buffer,
			Delivered:    counts[stressHandled],
			Handled:      counts[stressHandled] - failed,
			Failed:       failed,
			TimedOut:     counts[stressTimedOut],
			Dropped:      counts[stressDropped],
			HandleTime:   got.HandleTime,
		}
		if got != want || counts[stressClosed] != 0 {
			return fmt.Sprintf("got Stats %+v, want %+v, with %d messages reported closed, want 0", got, want, counts[stressClosed])
		}
	}

	return ""
}

// stressMessage returns ctx and options for a Broadcast: a random delivery, and a timeout on the message, on ctx, or
// none. cancel ends the ctx it returns.
func stressMessage(ctx context.Context) (context.Context, context.CancelFunc, []message.Option[int]) {
	deliveries := []message.Option[int]{
		message.WithSync[int](), message.WithParallel[int](), message.WithAsync[int](), message.WithNonBlocking[int](),
	}
	options := []message.Option[int]{deliveries[randN(len(deliveries))]}
	timeout := 1 + time.Duration(randN(int(stressTimeout)))
	switch randN(3) {
	case 0:
		options = append(options, message.WithTimeout[int](timeout))
	case 1:
		// Cancelled once Broadcast returns, so that async sends race it.
		ctx, cancel := context.WithTimeout(ctx, timeout)

		return ctx, cancel, options
	}

	return ctx, func() {}, options
}

// stressPause sleeps now and then, so that Broadcasts time out or drop messages.
func stressPause() {
	if randN(8) == 0 {
		time.Sleep(time.Duration(randN(int(stressTimeout))))
	}
}

// earliest stores tick in v, unless v holds an earlier one.
func earliest(v *atomic.Int64, tick int64) {
	for {
		old := v.Load()
		if (old != 0 && old <= tick) || v.CompareAndSwap(old, tick) {
			return
		}
	}
}

// tickOrNever returns the tick v holds, or the latest possible one if it holds none.
func tickOrNever(v *atomic.Int64) int64 {
	if tick := v.Load(); tick != 0 {
		return tick
	}

	return math.MaxInt64
}

// first returns the first few values, so that a failure does not print thousands.
func first[T any](values []T) []T {
	return values[:min(len(values), 5)]
}

// randN returns a random number in [0, n).
func randN(n int) int {
	return rand.IntN(n) //nolint:gosec // A test needs no cryptographic randomness.
}
