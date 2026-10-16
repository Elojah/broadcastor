package modbus_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elojah/sunspec/modbus"

	"github.com/elojah/broadcastor/middleware"
	"github.com/elojah/broadcastor/subscriber"
	transport "github.com/elojah/broadcastor/transport/modbus"
)

var errOther = errors.New("other")

func TestIsRetryable(t *testing.T) {
	t.Parallel()

	_, invalid := modbus.NewClient(modbus.NewLoopback(modbus.NewMemory(modbus.Tables{}))).
		ReadRegisters(t.Context(), unit, modbus.InputRegisters, 0, 0)
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"server device busy", exception(t, modbus.ErrServerDeviceBusy), true},
		{"gateway target failed", exception(t, modbus.ErrGatewayTargetFailed), true},
		{"illegal data address", exception(t, modbus.ErrIllegalDataAddress), false},
		{"server device failure", exception(t, modbus.ErrServerDeviceFailure), false},
		{"acknowledge", exception(t, modbus.ErrAcknowledge), false},
		{"timeout", context.DeadlineExceeded, true},
		{"CRC", fmt.Errorf("%w: frame 01 04", modbus.ErrCRC), true},
		{"invalid response", fmt.Errorf("%w: transaction 2", modbus.ErrInvalidResponse), true},
		{"EOF", io.EOF, true},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"dial", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{"invalid request", invalid, false},
		{"closed", modbus.ErrClosed, false},
		{"canceled", context.Canceled, false},
		{"other", errOther, false},
		{"nil", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := transport.IsRetryable(tt.err); got != tt.want {
				t.Errorf("IsRetryable(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

// With IsRetryable, middleware.Retry writes again to a busy device, but not to an address it does not have.
func TestIsRetryable_Retry(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		busy     bool
		address  uint16
		attempts int64
		err      error
	}{
		{"busy once", true, 0, 2, nil},
		{"illegal data address", false, 1, 1, modbus.ErrIllegalDataAddress},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				device := &busyOnce{Memory: modbus.NewMemory(modbus.Tables{Coils: make([]bool, 1)}), busy: tt.busy}
				client := modbus.NewClient(modbus.NewLoopback(device))
				b := newBroadcastor[bool](t)
				errs := make(chan error, 1)
				_, err := b.Subscribe(context.WithoutCancel(t.Context()),
					func(ctx context.Context, _ uint64, on bool) error {
						return client.WriteCoil(ctx, unit, tt.address, on)
					},
					subscriber.WithMiddleware(middleware.Retry[bool](middleware.RetryPolicy{
						Attempts: 3, Delay: time.Millisecond, IsRetryable: transport.IsRetryable,
					})),
					subscriber.WithErrorHandler[bool](func(_ context.Context, err error) { errs <- err }),
				)
				if err != nil {
					t.Fatalf("Subscribe: %v", err)
				}

				b.Broadcast(t.Context(), true)
				// Past Retry's waits.
				time.Sleep(time.Second)
				synctest.Wait()

				if n := device.writes.Load(); n != tt.attempts {
					t.Errorf("%d writes, want %d", n, tt.attempts)
				}
				select {
				case err := <-errs:
					if !errors.Is(err, tt.err) {
						t.Errorf("error handler got %v, want %v", err, tt.err)
					}
				default:
					if tt.err != nil {
						t.Errorf("error handler not called, want %v", tt.err)
					}
				}
			})
		})
	}
}

// exception returns the error a Client gets from a device answering a read with sentinel's exception.
func exception(t *testing.T, sentinel error) error {
	t.Helper()
	client := modbus.NewClient(modbus.NewLoopback(failing{Memory: modbus.NewMemory(modbus.Tables{}), err: sentinel}))
	_, err := client.ReadRegisters(t.Context(), unit, modbus.InputRegisters, 0, 1)
	if _, ok := errors.AsType[*modbus.ExceptionError](err); !ok || !errors.Is(err, sentinel) {
		t.Fatalf("ReadRegisters = %v, want an *modbus.ExceptionError matching %v", err, sentinel)
	}

	return err
}

// failing answers every read of registers with err.
type failing struct {
	*modbus.Memory

	err error
}

func (f failing) ReadRegisters(context.Context, uint8, modbus.Table, uint16, uint16) ([]uint16, error) {
	return nil, f.err
}

// busyOnce answers its first write busy if busy, and counts the writes.
type busyOnce struct {
	*modbus.Memory

	busy   bool
	writes atomic.Int64
}

func (d *busyOnce) WriteBits(ctx context.Context, unit uint8, address uint16, values []bool) error {
	if d.writes.Add(1) == 1 && d.busy {
		return modbus.ErrServerDeviceBusy
	}

	return d.Memory.WriteBits(ctx, unit, address, values)
}
