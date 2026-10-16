package modbus_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elojah/sunspec/modbus"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	transport "github.com/elojah/broadcastor/transport/modbus"
)

const (
	interval = time.Second
	unit     = 1
)

// Poll broadcasts a first reading at once, then one at each interval, until ctx is done.
func TestPoll(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		memory := modbus.NewMemory(modbus.Tables{InputRegisters: []uint16{1}})
		b := newBroadcastor[uint16](t)
		got := collect(t, b)
		ctx, cancel := context.WithCancel(t.Context())
		done := goPoll(ctx, b, readRegister(memory), failOnError(t))

		synctest.Wait()
		expect(t, got, 1)
		for _, want := range []uint16{2, 3} {
			memory.Update(func(tables *modbus.Tables) { tables.InputRegisters[0] = want })
			time.Sleep(interval)
			synctest.Wait()
			expect(t, got, want)
		}
		cancel()

		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Poll = %v, want %v", err, context.Canceled)
		}
		empty(t, got)
	})
}

// A failed read goes to onError and is not broadcast, and the next poll reads again.
func TestPoll_ReadError(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		memory := modbus.NewMemory(modbus.Tables{})
		b := newBroadcastor[uint16](t)
		got := collect(t, b)
		errs := make(chan error, 8)
		ctx, cancel := context.WithCancel(t.Context())
		done := goPoll(ctx, b, readRegister(memory), func(_ context.Context, err error) { errs <- err })

		synctest.Wait()
		if err := receiveErr(t, errs); !errors.Is(err, modbus.ErrIllegalDataAddress) {
			t.Errorf("onError got %v, want %v", err, modbus.ErrIllegalDataAddress)
		}
		empty(t, got)

		memory.Update(func(tables *modbus.Tables) { tables.InputRegisters = []uint16{7} })
		time.Sleep(interval)
		synctest.Wait()
		expect(t, got, 7)
		empty(t, errs)
		cancel()

		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Poll = %v, want %v", err, context.Canceled)
		}
	})
}

// Poll returns modbus.ErrClosed once the client's transport is closed, without reporting it.
func TestPoll_Closed(t *testing.T) {
	t.Parallel()

	client := modbus.NewClient(modbus.NewTCPTransport("127.0.0.1:502"))
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	read := func(ctx context.Context) (uint16, error) {
		registers, err := client.ReadRegisters(ctx, unit, modbus.InputRegisters, 0, 1)
		if err != nil {
			return 0, err
		}

		return registers[0], nil
	}

	b := newBroadcastor[uint16](t)
	if err := transport.Poll(t.Context(), b, interval, read, failOnError(t)); !errors.Is(err, modbus.ErrClosed) {
		t.Errorf("Poll = %v, want %v", err, modbus.ErrClosed)
	}
}

// A read failing because ctx is done ends Poll with ctx's error, without reporting it.
func TestPoll_Canceled(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := newBroadcastor[uint16](t)
		got := collect(t, b)
		ctx, cancel := context.WithCancel(t.Context())
		read := func(ctx context.Context) (uint16, error) {
			cancel()

			return 0, ctx.Err()
		}

		if err := transport.Poll(ctx, b, interval, read, failOnError(t)); !errors.Is(err, context.Canceled) {
			t.Errorf("Poll = %v, want %v", err, context.Canceled)
		}
		synctest.Wait()
		empty(t, got)
	})
}

// Poll broadcasts with its options.
func TestPoll_Options(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		memory := modbus.NewMemory(modbus.Tables{InputRegisters: []uint16{1}})
		b := newBroadcastor[uint16](t)
		got := collect(t, b)
		reads := make(chan struct{}, 8)
		read := readRegister(memory)
		ctx, cancel := context.WithCancel(t.Context())
		done := goPoll(ctx, b, func(ctx context.Context) (uint16, error) {
			reads <- struct{}{}

			return read(ctx)
		}, failOnError(t), message.WithSubscriberFilter[uint16](func(uint64) bool { return false }))

		synctest.Wait()
		expect(t, reads, struct{}{})
		empty(t, got)
		cancel()

		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Poll = %v, want %v", err, context.Canceled)
		}
	})
}

func newBroadcastor[T any](tb testing.TB) *broadcastor.Broadcastor[T] {
	tb.Helper()
	b := broadcastor.NewBroadcastor[T]()
	tb.Cleanup(func() { _ = b.Close() })

	return b
}

// collect subscribes a handle sending each message to the returned channel.
func collect[T any](t *testing.T, b *broadcastor.Broadcastor[T]) <-chan T {
	t.Helper()
	got := make(chan T, 8)
	// Never done, so that a leaked subscriber fails the test.
	_, err := b.Subscribe(context.WithoutCancel(t.Context()), func(_ context.Context, _ uint64, msg T) error {
		got <- msg

		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	return got
}

// readRegister returns a read of input register 0 of memory, through a Loopback.
func readRegister(memory *modbus.Memory) func(ctx context.Context) (uint16, error) {
	client := modbus.NewClient(modbus.NewLoopback(memory))

	return func(ctx context.Context) (uint16, error) {
		registers, err := client.ReadRegisters(ctx, unit, modbus.InputRegisters, 0, 1)
		if err != nil {
			return 0, err
		}

		return registers[0], nil
	}
}

// goPoll runs Poll every interval in a goroutine, and returns a channel receiving what it returns.
func goPoll(
	ctx context.Context, b transport.Broadcaster[uint16], read func(ctx context.Context) (uint16, error),
	onError func(ctx context.Context, err error), options ...message.Option[uint16],
) <-chan error {
	done := make(chan error, 1)
	go func() { done <- transport.Poll(ctx, b, interval, read, onError, options...) }()

	return done
}

func failOnError(t *testing.T) func(ctx context.Context, err error) {
	t.Helper()

	return func(_ context.Context, err error) {
		t.Errorf("onError got %v, want no call", err)
	}
}

// expect fails unless ch holds want already.
func expect[T comparable](t *testing.T, ch <-chan T, want T) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Errorf("received %v, want %v", got, want)
		}
	default:
		t.Errorf("received nothing, want %v", want)
	}
}

// receiveErr returns the error errs holds, which it must hold already.
func receiveErr(t *testing.T, errs <-chan error) error {
	t.Helper()
	select {
	case err := <-errs:
		return err
	default:
		t.Fatal("received no error")

		return nil
	}
}

// empty fails unless ch holds nothing.
func empty[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case v := <-ch:
		t.Errorf("received %v, want nothing", v)
	default:
	}
}
