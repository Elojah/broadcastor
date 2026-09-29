package udp_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/transport"
	"github.com/elojah/broadcastor/transport/udp"
)

// deadlockTimeout bounds every wait, so a deadlock fails the test instead of hanging. These tests use real sockets, so
// they run in real time rather than in a synctest bubble.
const deadlockTimeout = 10 * time.Second

var (
	errDecode = errors.New("not a message")
	errEncode = errors.New("cannot encode")
)

type datagram struct {
	text string
	from string
}

// broadcast is a Broadcast: its message and how its options configure it.
type broadcast struct {
	msg    datagram
	config message.Config
}

// publisher records every Broadcast.
type publisher chan broadcast

func (p publisher) Broadcast(_ context.Context, msg datagram, options ...message.Option[datagram]) int {
	p <- broadcast{msg: msg, config: message.New(msg, message.Config{}, options...).Config}

	return 1
}

func decode(b []byte, from net.Addr) (datagram, error) {
	if len(b) > 0 && b[0] == '!' {
		return datagram{}, errDecode
	}

	return datagram{text: string(b), from: from.String()}, nil
}

// Receive broadcasts every datagram, empty ones included, with the address it came from and without waiting by default,
// and returns ctx's error once ctx is done.
func TestReceive(t *testing.T) {
	t.Parallel()

	conn, peer := listen(t), listen(t)
	p := make(publisher, 3)
	ctx, cancel := context.WithCancel(t.Context())
	done := goReceive(ctx, conn, p)

	for _, text := range []string{"a", "", "b"} {
		write(t, peer, text, conn.LocalAddr())
		got := receive(t, p)
		if want := (datagram{text: text, from: peer.LocalAddr().String()}); got.msg != want {
			t.Errorf("Broadcast got %+v, want %+v", got.msg, want)
		}
		if got.config.Delivery != message.DeliveryNonBlocking {
			t.Errorf("Broadcast delivery = %v, want %v by default", got.config.Delivery, message.DeliveryNonBlocking)
		}
	}
	cancel()

	if err := receiveErr(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("Receive = %v, want %v", err, context.Canceled)
	}
}

// transport.WithMessageOptions replaces the default message options.
func TestReceive_MessageOptions(t *testing.T) {
	t.Parallel()

	conn, peer := listen(t), listen(t)
	p := make(publisher, 1)
	goReceive(t.Context(), conn, p, transport.WithMessageOptions(message.WithTimeout[datagram](time.Second)))

	write(t, peer, "a", conn.LocalAddr())
	got := receive(t, p)
	if got.config.Delivery != message.DeliverySync || got.config.Timeout != time.Second {
		t.Errorf("Broadcast delivery and timeout = %v and %v, want %v and %v",
			got.config.Delivery, got.config.Timeout, message.DeliverySync, time.Second)
	}
}

// A datagram decode fails on goes to the error handler, not to Broadcast, and Receive goes on.
func TestReceive_DecodeError(t *testing.T) {
	t.Parallel()

	conn, peer := listen(t), listen(t)
	p := make(publisher, 1)
	reported := make(chan error, 1)
	goReceive(t.Context(), conn, p, transport.WithErrorHandler[datagram](func(_ context.Context, err error) {
		reported <- err
	}))

	write(t, peer, "!", conn.LocalAddr())
	write(t, peer, "a", conn.LocalAddr())
	select {
	case err := <-reported:
		if !errors.Is(err, errDecode) {
			t.Errorf("error handler got %v, want %v", err, errDecode)
		}
	case <-time.After(deadlockTimeout):
		t.Fatalf("still waiting for the decode error after %v", deadlockTimeout)
	}
	if got := receive(t, p); got.msg.text != "a" {
		t.Errorf("Broadcast got %+v after the decode error, want a", got.msg)
	}
}

// Receive returns the read's error once conn is closed.
func TestReceive_Closed(t *testing.T) {
	t.Parallel()

	conn := listen(t)
	done := goReceive(t.Context(), conn, make(publisher))
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := receiveErr(t, done); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Receive = %v, want %v", err, net.ErrClosed)
	}
}

// Send writes every message to the address encode returns, and returns encode's error.
func TestSend(t *testing.T) {
	t.Parallel()

	conn, peer := listen(t), listen(t)
	handle := udp.Send(conn, func(msg string) ([]byte, net.Addr, error) {
		if msg == "" {
			return nil, nil, errEncode
		}

		return []byte(strings.ToUpper(msg)), peer.LocalAddr(), nil
	})

	if err := handle(t.Context(), uuid.New(), "a"); err != nil {
		t.Fatalf("handle = %v, want nil", err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(deadlockTimeout)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	b := make([]byte, 16)
	n, from, err := peer.ReadFrom(b)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if got, want := string(b[:n]), "A"; got != want || from.String() != conn.LocalAddr().String() {
		t.Errorf("peer read %q from %s, want %q from %s", got, from, want, conn.LocalAddr())
	}
	if err := handle(t.Context(), uuid.New(), ""); !errors.Is(err, errEncode) {
		t.Errorf("handle = %v, want %v", err, errEncode)
	}
}

// listen returns a conn on a free loopback port, closed when the test ends.
func listen(t *testing.T) net.PacketConn {
	t.Helper()
	conn, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

func write(t *testing.T, conn net.PacketConn, text string, to net.Addr) {
	t.Helper()
	if _, err := conn.WriteTo([]byte(text), to); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
}

func goReceive(ctx context.Context, conn net.PacketConn, p publisher, options ...transport.SourceOption[datagram]) <-chan error {
	done := make(chan error, 1)
	go func() { done <- udp.Receive(ctx, conn, p, decode, options...) }()

	return done
}

func receive(t *testing.T, p publisher) broadcast {
	t.Helper()
	select {
	case b := <-p:
		return b
	case <-time.After(deadlockTimeout):
		t.Fatalf("still waiting for Broadcast after %v", deadlockTimeout)

		return broadcast{}
	}
}

func receiveErr(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(deadlockTimeout):
		t.Fatalf("still waiting for Receive after %v: deadlock", deadlockTimeout)

		return nil
	}
}
