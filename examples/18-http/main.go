// HTTP in and out: http.Receive broadcasts every reading POSTed to /readings, and http.Stream sends each client of
// GET /readings?room=... the readings of its room as server-sent events, picked with subscriber.WithFilter. A client
// that reads slowly never holds a POST up: once its ring is full, it loses its oldest readings.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/subscriber"
	httptransport "github.com/elojah/broadcastor/transport/http"
)

type reading struct {
	Room    string  `json:"room"`
	Celsius float64 `json:"celsius"`
}

func decode(r *http.Request) (reading, error) {
	var rd reading
	err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<10)).Decode(&rd)

	return rd, err
}

// encode makes the room the event's type, which a browser's EventSource listens to.
func encode(rd reading) (httptransport.Event, error) {
	data, err := json.Marshal(rd)

	return httptransport.Event{Type: rd.Room, Data: data}, err
}

func main() {
	ctx := context.Background()
	b := broadcastor.NewBroadcastor[reading]()
	mux := http.NewServeMux()
	mux.Handle("POST /readings", httptransport.Receive(b, decode))
	mux.HandleFunc("GET /readings", func(w http.ResponseWriter, r *http.Request) {
		room := r.URL.Query().Get("room")
		_ = httptransport.Stream(w, r, b, encode, httptransport.WithSubscriberOptions(
			subscriber.WithFilter(func(rd reading) bool { return rd.Room == room }),
		))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Subscribed once it has the headers.
	stream := do(ctx, srv, http.MethodGet, "/readings?room=kitchen", "")
	for _, body := range []string{
		`{"room":"kitchen","celsius":21.5}`,
		`{"room":"garage","celsius":8}`,
		`{"room":"kitchen","celsius":22}`,
		`not json`,
	} {
		resp := do(ctx, srv, http.MethodPost, "/readings", body)
		if err := resp.Body.Close(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("POST", body, resp.Status)
	}

	events := bufio.NewScanner(stream.Body)
	for n := 0; n < 2 && events.Scan(); {
		if events.Text() == "" {
			n++

			continue
		}
		fmt.Println(events.Text())
	}
	if err := stream.Body.Close(); err != nil {
		log.Fatal(err)
	}
}

func do(ctx context.Context, srv *httptest.Server, method, path, body string) *http.Response {
	req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		log.Fatal(err)
	}

	return resp
}
