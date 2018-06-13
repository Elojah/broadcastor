package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"net/http"

	"github.com/go-kit/kit/endpoint"
	log "github.com/sirupsen/logrus"

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
	log.WithField("room", room.ID).Info("room found")
	if _, err := m.GetUser(bc.UserSubset{
		ID:     msg.UserID,
		RoomID: msg.RoomID,
	}); err != nil {
		return err
	}
	log.WithField("user", msg.UserID).Info("user found")
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
			log.WithField("message", msg.ID).
				WithField("pool", pool.String()).
				WithField("address", address).
				Info("spread message")
			resp, err := m.client.Post(
				address+"/message/send",
				"application/json; charset=utf-8",
				bytes.NewBuffer(raw),
			)
			if err != nil {
				log.WithError(err).Error("failed to spread message")
				return
			}
			log.WithField("code", resp.StatusCode).Info("spreader response code")
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
