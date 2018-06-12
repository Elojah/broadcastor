package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"

	"github.com/go-kit/kit/endpoint"
	"github.com/oklog/ulid"

	bc "github.com/elojah/broadcastor"
)

// UserService interface user routes.
type UserService interface {
	Create(context.Context, bc.ID) (bc.ID, error)
}

type user struct {
	bc.RoomMapper
	bc.UserMapper
}

func (u user) pickPool(pools []bc.ID) bc.ID {
	return pools[rand.Intn(len(pools))]
}

func (u user) Create(_ context.Context, roomID bc.ID) (bc.ID, error) {
	room, err := u.GetRoom(bc.RoomSubset{ID: roomID})
	if err != nil {
		return bc.ID{}, err
	}
	user := bc.User{ID: bc.NewID()}
	return user.ID, u.AddUser(user, roomID, u.pickPool(room.Pools))
}

func (u user) MakeCreateEndpoint() endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		req := request.(bc.ID)
		return u.Create(ctx, req)
	}
}

func (u user) DecodeReq(_ context.Context, req *http.Request) (interface{}, error) {
	var request string
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		return nil, err
	}
	return ulid.Parse(request)
}

func (u user) EncodeResp(ctx context.Context, w http.ResponseWriter, response interface{}) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	return json.NewEncoder(w).Encode(response)
}
