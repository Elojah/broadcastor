package http_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/message"
	"github.com/elojah/broadcastor/subscriber"
	"github.com/elojah/broadcastor/transport"
	httptransport "github.com/elojah/broadcastor/transport/http"
)

// deadlockTimeout bounds every wait, so a deadlock fails the test instead of hanging.
const deadlockTimeout = 10 * time.Second

var (
	errDecode = errors.New("not a message")
	errEncode = errors.New("cannot encode")
)

// broadcast is a Broadcast: its message and how its options configure it.
type broadcast struct {
	msg    string
	config message.Config
}

// publisher records every Broadcast.
type publisher struct {
	mu  sync.Mutex
	got []broadcast
}

func (p *publisher) Broadcast(_ context.Context, msg string, options ...message.Option[string]) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, broadcast{msg: msg, config: message.New(msg, message.Config{}, options...).Config})

	return 1
}

// subscribable records the ctx of every Subscribe.
type subscribable struct {
	*broadcastor.Broadcastor[string]

	ctxs chan context.Context
}

func (s subscribable) Subscribe(
	ctx context.Context, handle func(context.Context, uuid.UUID, string) error, options ...subscriber.Option[string],
) (uuid.UUID, error) {
	s.ctxs <- ctx

	return s.Broadcastor.Subscribe(ctx, handle, options...)
}

// blockingWriter is a ResponseWriter whose writes wait until it is released.
type blockingWriter struct {
	*httptest.ResponseRecorder

	released chan struct{}
	mu       sync.Mutex
}

func (w *blockingWriter) Write(b []byte) (int, error) {
	<-w.released
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.ResponseRecorder.Write(b)
}

func (w *blockingWriter) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.Body.String()
}

func decode(r *http.Request) (string, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errDecode
	}

	return string(b), nil
}

func encode(msg string) (httptransport.Event, error) {
	return httptransport.Event{Data: []byte(msg)}, nil
}

// Receive broadcasts what decode makes of each request with message.WithSync by default and answers 202, or answers
// 400 with decode's error, which the error handler also gets.
func TestReceive(t *testing.T) {
	t.Parallel()

	p := &publisher{}
	var reported []error
	handler := httptransport.Receive(p, decode, transport.WithErrorHandler[string](func(_ context.Context, err error) {
		reported = append(reported, err)
	}))

	w := httptest.NewRecorder()
	handler(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader("a")))
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	w = httptest.NewRecorder()
	handler(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", http.NoBody))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errDecode.Error()) {
		t.Errorf("status = %d and body %q, want %d and %v", w.Code, w.Body, http.StatusBadRequest, errDecode)
	}

	if len(p.got) != 1 || p.got[0].msg != "a" || p.got[0].config.Delivery != message.DeliverySync {
		t.Errorf("Broadcast got %+v, want only a, with %v", p.got, message.DeliverySync)
	}
	if len(reported) != 1 || !errors.Is(reported[0], errDecode) {
		t.Errorf("error handler got %v, want %v", reported, errDecode)
	}
}

// Stream is subscribed once the client has the headers, sends every message as an event, one data field per line,
// and returns once the client leaves, which unsubscribes it.
func TestStream(t *testing.T) {
	t.Parallel()

	b := broadcastor.NewBroadcastor[string]()
	s := subscribable{Broadcastor: b, ctxs: make(chan context.Context, 1)}
	done := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done <- httptransport.Stream(w, r, s, func(msg string) (httptransport.Event, error) {
			return httptransport.Event{ID: "1", Type: "text", Data: []byte(msg)}, nil
		})
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}

	for _, msg := range []string{"a", "b\nc", "d\r\ne\rf"} {
		if n := b.Broadcast(t.Context(), msg); n != 1 {
			t.Errorf("Broadcast(%q) = %d, want 1 once the client has the headers", msg, n)
		}
	}
	want := "id: 1\nevent: text\ndata: a\n\n" +
		"id: 1\nevent: text\ndata: b\ndata: c\n\n" +
		"id: 1\nevent: text\ndata: d\ndata: e\ndata: f\n\n"
	if got := readEvents(t, resp.Body, 3); got != want {
		t.Errorf("client got %q, want %q", got, want)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Stream = %v, want %v", err, context.Canceled)
		}
	case <-time.After(deadlockTimeout):
		t.Fatalf("still waiting for Stream after %v: deadlock", deadlockTimeout)
	}
	if subscribeCtx := <-s.ctxs; subscribeCtx.Err() == nil {
		t.Error("Stream returned with its subscription's ctx not done, want it unsubscribed")
	}
}

// A client that stops reading holds no Broadcast up, even a parallel one with a timeout: Stream keeps the newest
// messages that fit in its ring, and sends them once the client reads again.
func TestStream_SlowClient(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := broadcastor.NewBroadcastor[string]()
		w := &blockingWriter{ResponseRecorder: httptest.NewRecorder(), released: make(chan struct{})}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", http.NoBody)
			done <- httptransport.Stream(w, r, b, encode, httptransport.WithRingSize[string](2))
		}()
		synctest.Wait()

		b.Broadcast(t.Context(), "0")
		synctest.Wait() // the write of 0 waits for the client
		start := time.Now()
		for _, msg := range []string{"1", "2", "3", "4", "5"} {
			n := b.Broadcast(t.Context(), msg, message.WithParallel[string](), message.WithTimeout[string](time.Second),
				message.WithErrorHandler[string](func(_ context.Context, err error) { t.Errorf("error handler got %v", err) }))
			if n != 1 {
				t.Errorf("Broadcast(%s) = %d, want 1", msg, n)
			}
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("Broadcast waited %v for a client that does not read, want 0", waited)
		}
		synctest.Wait()
		close(w.released)
		synctest.Wait()
		cancel()

		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Stream = %v, want %v", err, context.Canceled)
		}
		if got, want := w.body(), "data: 0\n\ndata: 4\n\ndata: 5\n\n"; got != want {
			t.Errorf("client got %q, want %q", got, want)
		}
	})
}

// An event encode fails on, or whose type holds a line break, ends the stream with that error.
func TestStream_EncodeError(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		event httptransport.Event
		err   error
		want  error
	}{
		{"encode", httptransport.Event{}, errEncode, errEncode},
		{"line break", httptransport.Event{Type: "a\nb"}, nil, httptransport.ErrEventField},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				b := broadcastor.NewBroadcastor[string]()
				w := httptest.NewRecorder()
				done := make(chan error, 1)
				go func() {
					r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
					done <- httptransport.Stream(w, r, b, func(string) (httptransport.Event, error) { return tt.event, tt.err })
				}()
				synctest.Wait()
				b.Broadcast(t.Context(), "a")

				if err := <-done; !errors.Is(err, tt.want) {
					t.Errorf("Stream = %v, want %v", err, tt.want)
				}
				if w.Body.Len() != 0 {
					t.Errorf("client got %q, want nothing", w.Body)
				}
			})
		})
	}
}

// Stream answers 503 once the Broadcastor is closed.
func TestStream_Closed(t *testing.T) {
	t.Parallel()

	b := broadcastor.NewBroadcastor[string]()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)

	if err := httptransport.Stream(w, r, b, encode); !errors.Is(err, broadcastor.ErrClosed) {
		t.Errorf("Stream = %v, want %v", err, broadcastor.ErrClosed)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

// With a write timeout, a client that stops reading ends the stream once the connection's buffers are full.
func TestStreamWithWriteTimeout(t *testing.T) {
	t.Parallel()

	b := broadcastor.NewBroadcastor[string]()
	done := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done <- httptransport.Stream(w, r, b, encode, httptransport.WithWriteTimeout[string](100*time.Millisecond))
	}))
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Far more than the connection's buffers hold, and than the ring does, so the ring is never empty.
	big := strings.Repeat("x", 1<<20)
	for range 64 {
		b.Broadcast(t.Context(), big)
	}
	select {
	case err := <-done:
		if err == nil || errors.Is(err, context.Canceled) {
			t.Errorf("Stream = %v, want a write error", err)
		}
	case <-time.After(deadlockTimeout):
		t.Fatalf("Stream still writing to a client that does not read after %v", deadlockTimeout)
	}
}

// readEvents reads n events from r.
func readEvents(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	var got bytes.Buffer
	lines := bufio.NewScanner(r)
	for n > 0 && lines.Scan() {
		got.WriteString(lines.Text() + "\n")
		if lines.Text() == "" {
			n--
		}
	}
	if err := lines.Err(); err != nil {
		t.Fatalf("reading events: %v", err)
	}

	return got.String()
}
