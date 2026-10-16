package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"

	"github.com/elojah/sunspec/modbus"
)

var _ modbus.Handler = (*plc)(nil)

// plc simulates a PLC: input register temperatureRegister holds its temperature, and coil fanCoil drives its fan. Each
// read of the register returns its next reading, then the last one again. It answers its first write with exception 6,
// server device busy, as a PLC starting up does, and never answers its third, as if it were gone, until the server
// stops. The server calls it from a goroutine per connection.
type plc struct {
	// The fan's coil.
	*modbus.Memory

	mu       sync.Mutex
	readings []uint16
	writes   atomic.Int64
}

func newPLC(readings []uint16) *plc {
	return &plc{Memory: modbus.NewMemory(modbus.Tables{Coils: make([]bool, 1)}), readings: readings}
}

// ReadRegisters returns the temperature.
func (p *plc) ReadRegisters(_ context.Context, _ uint8, table modbus.Table, address, quantity uint16) ([]uint16, error) {
	if table != modbus.InputRegisters || address != temperatureRegister || quantity != 1 {
		return nil, modbus.ErrIllegalDataAddress
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	tenths := p.readings[0]
	if len(p.readings) > 1 {
		p.readings = p.readings[1:]
	}

	return []uint16{tenths}, nil
}

// WriteBits writes the fan.
func (p *plc) WriteBits(ctx context.Context, unit uint8, address uint16, values []bool) error {
	switch p.writes.Add(1) {
	case 1:
		fmt.Println("plc: busy")

		return modbus.ErrServerDeviceBusy
	case 3:
		fmt.Println("plc: not answering")
		// The server reads the connection's next request only once this returns, and cancels ctx as it stops.
		<-ctx.Done()

		return ctx.Err()
	}

	return p.Memory.WriteBits(ctx, unit, address, values)
}

// servePLC serves p over Modbus TCP on a free local port, and returns its address and stop, which stops the server,
// ending the write p never answers, and returns what it returned.
func servePLC(ctx context.Context, p *plc) (string, func() error) {
	l, err := new(net.ListenConfig).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- modbus.Serve(serveCtx, l, p) }()

	return l.Addr().String(), func() error {
		cancel()

		return <-served
	}
}
