// Package http carries messages over HTTP: Receive is a handler that broadcasts every request it gets, and Stream sends
// every message to a client as server-sent events. Import it under another name, such as httptransport, next to net/http.
package http

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
	"github.com/elojah/broadcastor/transport"
)

// ErrEventField is returned for an Event whose ID or Type holds a line break, which would end the field early.
var ErrEventField = errors.New("event ID or type holds a line break")

// Event is one server-sent event.
type Event struct {
	// ID sets the client's last event ID, unless it is empty.
	ID string

	// Type is the event's type, which the client listens to, unless it is empty (a "message" event).
	Type string

	// Data is the event's data, split into as many data fields as it has lines.
	Data []byte
}

// marshal returns e as it goes on the wire, blank line included, or ErrEventField.
func (e Event) marshal() ([]byte, error) {
	if strings.ContainsAny(e.ID, "\r\n") || strings.ContainsAny(e.Type, "\r\n") {
		return nil, ErrEventField
	}
	var b []byte
	if e.ID != "" {
		b = append(append(append(b, "id: "...), e.ID...), '\n')
	}
	if e.Type != "" {
		b = append(append(append(b, "event: "...), e.Type...), '\n')
	}
	// Clients end a line at CR, LF or CRLF, so each one starts a new data field.
	data := bytes.ReplaceAll(bytes.ReplaceAll(e.Data, []byte("\r\n"), []byte("\n")), []byte("\r"), []byte("\n"))
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		b = append(append(append(b, "data: "...), line...), '\n')
	}

	return append(b, '\n'), nil
}

// StreamOption configures Stream.
type StreamOption[T any] func(config *streamConfig[T])

// streamConfig is what StreamOptions set.
type streamConfig[T any] struct {
	size         int
	writeTimeout time.Duration
	options      []subscriber.Option[T]
}

// Receive returns a handler that decodes every request into a message and broadcasts it to p, with the request's ctx.
// It answers 202 Accepted once Broadcast returns, or 400 Bad Request with decode's error, which also goes to the error
// handler (transport.WithErrorHandler). Route it with a method, such as "POST /events", and bound the body in decode,
// with http.MaxBytesReader.
//
// By default it broadcasts with message.WithSync, so the request waits for every subscriber to take the message, and
// ends the wait if the client leaves. A subscriber whose default is message.WithAsync would otherwise miss the messages
// it had not taken when the request's ctx ends.
func Receive[T any](p transport.Publisher[T], decode func(r *http.Request) (T, error), options ...transport.SourceOption[T]) http.HandlerFunc {
	config := transport.NewSourceConfig([]message.Option[T]{message.WithSync[T]()}, options...)

	return func(w http.ResponseWriter, r *http.Request) {
		msg, err := decode(r)
		if err != nil {
			config.Report(r.Context(), err)
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}
		p.Broadcast(r.Context(), msg, config.MessageOptions...)
		w.WriteHeader(http.StatusAccepted)
	}
}

// Stream sends every message s broadcasts to the client as a server-sent event, which encode makes, until the
// request's ctx is done or a write fails, and returns why. It subscribes before answering, so each message broadcast
// once the client has the response's headers reaches it.
//
// The subscriber only puts each message in a store.Ring of its own (store.Enqueue), which Stream drains to the client.
// So a slow client never holds a Broadcast up, whatever its options: once its ring is full, it loses its oldest
// messages. The subscription's ctx ends with the stream, which unsubscribes it.
//
// If Subscribe fails, it answers 503 Service Unavailable. An error encode returns ends the stream.
func Stream[T any](
	w http.ResponseWriter, r *http.Request, s transport.Subscribable[T], encode func(msg T) (Event, error), options ...StreamOption[T],
) error {
	config := streamConfig[T]{size: 64}
	for _, option := range options {
		option(&config)
	}
	rc := http.NewResponseController(w)
	// Cancelled when the stream ends, which unsubscribes.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	ring := store.NewRing[T](config.size)
	if _, err := s.Subscribe(ctx, store.Enqueue(ring), config.options...); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)

		return err
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	// The headers are out, so there is no status left to answer with.
	if err := rc.Flush(); err != nil {
		return err
	}

	// Drain goes on after an error, so the first one ends the stream through ctx. net/http may cancel r's ctx first.
	var sendErr error
	err := store.Drain(ctx, ring, func(_ context.Context, _ uuid.UUID, msg T) error {
		if sendErr = send(rc, w, encode, msg, config.writeTimeout); sendErr != nil {
			cancel()
		}

		return sendErr
	}, nil)
	if sendErr != nil {
		return sendErr
	}

	return err
}

// WithRingSize sets how many messages Stream keeps for a client that reads slower than they come, 64 by default.
func WithRingSize[T any](size int) StreamOption[T] {
	return func(config *streamConfig[T]) {
		config.size = size
	}
}

// WithWriteTimeout bounds each event's write, so that a client that stopped reading ends the stream. 0 means none,
// the default, and leaves Stream waiting until the connection breaks.
func WithWriteTimeout[T any](timeout time.Duration) StreamOption[T] {
	return func(config *streamConfig[T]) {
		config.writeTimeout = timeout
	}
}

// WithSubscriberOptions sets the options Stream subscribes with, such as subscriber.WithFilter for the messages the
// request asks for. subscriber.WithDetachedContext would keep the subscriber once the stream ends.
func WithSubscriberOptions[T any](options ...subscriber.Option[T]) StreamOption[T] {
	return func(config *streamConfig[T]) {
		config.options = options
	}
}

// send encodes msg and writes it to w as an event, within timeout unless it is 0.
func send[T any](rc *http.ResponseController, w http.ResponseWriter, encode func(msg T) (Event, error), msg T, timeout time.Duration) error {
	e, err := encode(msg)
	if err != nil {
		return err
	}
	b, err := e.marshal()
	if err != nil {
		return err
	}
	if timeout > 0 {
		if err := rc.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
	}
	if _, err := w.Write(b); err != nil {
		return err
	}

	return rc.Flush()
}
