package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"strings"

	"github.com/go-kit/kit/endpoint"
	"github.com/oklog/ulid"

	bc "github.com/elojah/broadcastor"
)

// UserService interface user routes.
type UserService interface {
	Create(context.Context, userRequest) (bc.ID, error)
}

type user struct {
	bc.RoomMapper
	bc.UserMapper

	clientPort string
}

type userRequest struct {
	RoomID bc.ID
	Addr   string
}

// TODO use something smarter instead than rand.
func (u user) pickPool(pools []bc.ID) bc.ID {
	return pools[rand.Intn(len(pools))]
}

func (u user) Create(_ context.Context, ur userRequest) (bc.ID, error) {
	room, err := u.GetRoom(bc.RoomSubset{ID: ur.RoomID})
	if err != nil {
		return bc.ID{}, err
	}
	user := bc.User{ID: bc.NewID(), Addr: ur.Addr}
	return user.ID, u.AddUser(user, ur.RoomID, u.pickPool(room.Pools))
}

func (u user) MakeCreateEndpoint() endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		req := request.(userRequest)
		return u.Create(ctx, req)
	}
}

func (u user) DecodeReq(_ context.Context, req *http.Request) (interface{}, error) {
	var request string
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		return nil, err
	}
	id, err := ulid.Parse(request)
	if err != nil {
		return nil, err
	}
	addrs := strings.Split(req.RemoteAddr, ":")
	addr := strings.Join([]string{addrs[0], u.clientPort}, ":")
	return userRequest{RoomID: id, Addr: addr}, nil
}

func (u user) EncodeResp(ctx context.Context, w http.ResponseWriter, response interface{}) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	return json.NewEncoder(w).Encode(response)
}
