package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-kit/kit/endpoint"

	bc "github.com/elojah/broadcastor"
)

// MessageService interface message routes.
type MessageService interface {
	Receive(context.Context, bc.Message) error
}

type message struct {
	callback func(string)
}

func (m message) Receive(_ context.Context, msg bc.Message) error {
	m.callback(time.Unix(int64(msg.ID.Time()/1000), 0).Format("Mon Jan 2 15:04:05 MST 2006") +
		" | " + msg.Content)
	return nil
}

func (m message) MakeReceiveEndpoint() endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		return nil, m.Receive(ctx, request.(bc.Message))
	}
}

func (m message) DecodeReq(_ context.Context, req *http.Request) (interface{}, error) {
	var request bc.Message
	err := json.NewDecoder(req.Body).Decode(&request)
	return request, err
}

func (m message) EncodeResp(ctx context.Context, w http.ResponseWriter, response interface{}) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	return nil
}
