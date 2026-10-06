package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"

	"github.com/simonvetter/modbus"
)

var _ modbus.RequestHandler = (*plc)(nil)

// plc stands in for a PLC on the Modbus network: input register temperatureRegister holds its temperature, and coil
// fanCoil drives its fan. Each read of the register returns the next of its readings, then the last one again. It
// answers its first write with exception 6, server device busy, as a PLC does while it is starting up, and never
// answers its third, nor any later request on that connection, as if the connection were gone. The server calls it
// from a goroutine per client connection.
type plc struct {
	mu       sync.Mutex
	readings []uint16
	writes   atomic.Int64
	fan      atomic.Bool
	// stopped ends the write the PLC never answers.
	stopped chan struct{}
}

func newPLC(readings []uint16) *plc {
	return &plc{readings: readings, stopped: make(chan struct{})}
}

// HandleCoils reads or writes the fan.
func (p *plc) HandleCoils(req *modbus.CoilsRequest) ([]bool, error) {
	if req.Addr != fanCoil || req.Quantity != 1 {
		return nil, modbus.ErrIllegalDataAddress
	}
	if !req.IsWrite {
		return []bool{p.fan.Load()}, nil
	}
	switch p.writes.Add(1) {
	case 1:
		fmt.Println("plc: busy")

		return nil, modbus.ErrServerDeviceBusy
	case 3:
		// The server reads the connection's next request only once this returns, so it answers none until the PLC
		// stops. ErrProtocolError then closes the connection without an answer.
		<-p.stopped

		return nil, modbus.ErrProtocolError
	}
	p.fan.Store(req.Args[0])

	return nil, nil
}

// HandleDiscreteInputs refuses every request: the PLC has none.
func (p *plc) HandleDiscreteInputs(*modbus.DiscreteInputsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalFunction
}

// HandleHoldingRegisters refuses every request: the PLC has none.
func (p *plc) HandleHoldingRegisters(*modbus.HoldingRegistersRequest) ([]uint16, error) {
	return nil, modbus.ErrIllegalFunction
}

// HandleInputRegisters returns the temperature.
func (p *plc) HandleInputRegisters(req *modbus.InputRegistersRequest) ([]uint16, error) {
	if req.Addr != temperatureRegister || req.Quantity != 1 {
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

// servePLC serves p over Modbus TCP on a free local port, and returns its address and stop, which ends the write p
// never answers and stops the server. The server listens on the address it is given, without saying which port it
// took, so servePLC first takes a free one from the kernel.
func servePLC(ctx context.Context, p *plc) (string, func() error) {
	l, err := new(net.ListenConfig).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		log.Fatal(err)
	}

	server, err := modbus.NewServer(&modbus.ServerConfiguration{
		URL: "tcp://" + addr,
		// It logs to stdout without one.
		Logger: log.New(io.Discard, "", 0),
	}, p)
	if err != nil {
		log.Fatal(err)
	}
	if err := server.Start(); err != nil {
		log.Fatal(err)
	}

	return addr, func() error {
		close(p.stopped)

		return server.Stop()
	}
}
