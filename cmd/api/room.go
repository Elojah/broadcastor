package main

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-kit/kit/endpoint"

	bc "github.com/elojah/broadcastor"
)

// RoomService interface room routes.
type RoomService interface {
	Create(context.Context) (bc.ID, error)
	ListIDs(context.Context) ([]bc.ID, error)
}

type room struct {
	bc.RoomMapper
	pools uint
}

func (r room) newPools() []bc.ID {
	pools := make([]bc.ID, r.pools)
	for i := range pools {
		pools[i] = bc.NewID()
	}
	return pools
}

func (r room) Create(_ context.Context) (bc.ID, error) {
	room := bc.Room{ID: bc.NewID()}
	return room.ID, r.CreateRoom(room)
}

func (r room) ListIDs(_ context.Context) ([]bc.ID, error) {
	return r.ListRoomIDs()
}

func (r room) MakeCreateEndpoint() endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		return r.Create(ctx)
	}
}

func (r room) MakeListIDsEndpoint() endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		return r.ListIDs(ctx)
	}
}

func (r room) DecodeReq(_ context.Context, req *http.Request) (interface{}, error) {
	return nil, nil
}

func (r room) EncodeResp(ctx context.Context, w http.ResponseWriter, response interface{}) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	return json.NewEncoder(w).Encode(response)
}
