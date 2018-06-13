package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
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

	spreaders []string
	client    *http.Client
}

func (m message) Send(_ context.Context, msg bc.Message) error {
	room, err := m.GetRoom(bc.RoomSubset{ID: msg.RoomID})
	if err != nil {
		return err
	}
	if _, err := m.GetUser(bc.UserSubset{
		ID:     msg.UserID,
		RoomID: msg.RoomID,
	}); err != nil {
		return err
	}
	msg.ID = bc.NewID()
	if err := m.CreateMessage(msg); err != nil {
		return err
	}

	for _, pool := range room.Pools {
		go func(pool bc.ID) {
			raw, _ := json.Marshal(bc.MessageRequest{
				MessageID: msg.ID,
				RoomID:    room.ID,
				PoolID:    pool,
			})
			// TODO use something smarter instead than rand.
			address := m.spreaders[rand.Intn(len(m.spreaders))]
			m.client.Post(address+"/message/send", "application/json; charset=utf-8", bytes.NewBuffer(raw))
		}(pool)
	}
	return nil
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
