// Package modbus connects a Broadcastor to Modbus devices through a github.com/elojah/sunspec/modbus Client, which
// stays the caller's: Poll broadcasts what a device reads, a subscriber's handle writes to one with the Client, and
// IsRetryable tells middleware.Retry which failures to retry.
//
//	client := modbus.NewClient(modbus.NewTCPTransport("192.168.1.10:502"))
//	go transport.Poll(ctx, b, time.Second, func(ctx context.Context) ([]uint16, error) {
//		return client.ReadRegisters(ctx, 1, modbus.InputRegisters, 0, 2)
//	}, logError)
//	id, err := b.Subscribe(ctx, handle, subscriber.WithMiddleware(
//		middleware.Retry[[]uint16](middleware.RetryPolicy{Attempts: 3, Delay: 100 * time.Millisecond, IsRetryable: transport.IsRetryable}),
//	))
//
// A TCPTransport dials again on the request after a failure, so a retry reconnects. The package is named like sunspec's,
// so import one of them under another name, such as transport.
//
// It is a module of its own, at v0, so that only its users depend on sunspec.
package modbus
