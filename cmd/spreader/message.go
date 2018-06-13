package main

import (
	"bytes"
	"context"
	"encoding/json"
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

	client *http.Client
	count  int64
}

func (m message) Send(_ context.Context, mr bc.MessageRequest) error {
	msg, err := m.GetMessage(bc.MessageSubset{ID: mr.MessageID})
	if err != nil {
		log.WithError(err).WithField("message", mr.MessageID.String()).Error("failed to retrieve message")
		return err
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	var addrs []string
	var cursor uint64
	log.WithField("message", msg.ID.String()).WithField("pool", mr.PoolID).Info("send message")
	for {
		addrs, cursor, err = m.ListUserAddr(bc.UserSubset{
			Cursor: cursor,
			Count:  m.count,
			RoomID: mr.RoomID,
			PoolID: mr.PoolID,
		})
		if err != nil {
			log.WithError(err).Error("failed to retrieve users address")
			return err
		}
		log.WithField("n_addrs", len(addrs)).Info("found addresses")
		go func(addrs ...string) {
			for _, addr := range addrs {
				log.Infof("send message %s to %s", msg.ID.String(), addr)
				m.client.Post("http://"+addr+"/message/receive", "application/json; charset=utf-8", bytes.NewBuffer(raw))
			}
		}(addrs...)
		if cursor == 0 {
			return nil
		}
	}
}

func (m message) MakeSendEndpoint() endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		return nil, m.Send(ctx, request.(bc.MessageRequest))
	}
}

func (m message) DecodeReq(_ context.Context, req *http.Request) (interface{}, error) {
	var request bc.MessageRequest
	err := json.NewDecoder(req.Body).Decode(&request)
	return request, err
}

func (m message) EncodeResp(ctx context.Context, w http.ResponseWriter, response interface{}) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	return nil
}
