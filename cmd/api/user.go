package main

import (
	"context"
	"encoding/json"
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
	bc.UserMapper
}

func (u user) Create(_ context.Context, roomID bc.ID) (bc.ID, error) {
	user := bc.User{ID: bc.NewID()}
	return user.ID, u.AddUser(user, roomID)
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
