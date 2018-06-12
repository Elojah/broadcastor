package main

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-kit/kit/endpoint"

	bc "github.com/elojah/broadcastor"
)

// MessageService interface message routes.
type MessageService interface {
	Send(context.Context, bc.Message) error
}

type message struct {
	bc.UserMapper
	bc.MessageMapper
	bc.RoomMapper
}

func (m message) Send(_ context.Context, msg bc.Message) error {
	if _, err := m.GetUser(bc.UserSubset{
		ID:     msg.UserID,
		RoomID: msg.RoomID,
	}); err != nil {
		return err
	}
	msg.ID = bc.NewID()
	return m.CreateMessage(msg)
}

func (m message) MakeSendEndpoint() endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		return nil, m.Send(ctx, request.(bc.Message))
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
