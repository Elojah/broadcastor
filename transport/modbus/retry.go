package modbus

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/elojah/sunspec/modbus"
)

// IsRetryable reports whether a request that failed with err may succeed if sent again, for
// middleware.RetryPolicy.IsRetryable: a device busy (modbus.ServerDeviceBusy), one behind a gateway not answering
// (modbus.GatewayTargetFailed), a request timed out (context.DeadlineExceeded), a response garbled (modbus.ErrCRC,
// modbus.ErrInvalidResponse) or cut (io.EOF, io.ErrUnexpectedEOF), or a connection failed (a net.Error), which a
// TCPTransport dials again. Any other error is the request's, the device's, or the caller's, and sending it again
// fails the same.
//
// A write that timed out may have been applied: retry only writes that are safe to repeat, such as setting a value
// rather than toggling one.
func IsRetryable(err error) bool {
	if e, ok := errors.AsType[*modbus.ExceptionError](err); ok {
		return e.Code == modbus.ServerDeviceBusy || e.Code == modbus.GatewayTargetFailed
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}

	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, modbus.ErrCRC) ||
		errors.Is(err, modbus.ErrInvalidResponse) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}
