package main

import (
	"context"

	grpc "github.com/go-kit/kit/transport/grpc"

	bc "github.com/elojah/broadcastor"
	"github.com/elojah/broadcastor/dto"
)

// RoomService interface room routes.
type RoomService interface {
	New(context.Context) (bc.ID, error)
	ListIDs(context.Context) ([]bc.ID, error)
}

type room struct {
	bc.RoomMapper
}

func (r room) Create(_ context.Context) (bc.ID, error) {
	room := bc.Room{ID: bc.NewID()}
	return room.ID, r.CreateRoom(room)
}

func (r room) ListIDs(_ context.Context) ([]bc.ID, error) {
	return r.ListRoomIDs()
}

// GRPCRoomService wraps room with grpc.
type GRPCRoomService struct {
	create  grpc.Handler
	listIDs grpc.Handler
}

// Create serve grpc CreateRoom.
func (s *GRPCRoomService) Create(ctx context.Context) (*dto.ID, error) {
	_, resp, err := s.listIDs.ServeGRPC(ctx, nil)
	if err != nil {
		return nil, err
	}
	return resp.(*dto.ID), nil
}

// ListIDs serve grpc ListIDsRoom.
func (s *GRPCRoomService) ListIDs(ctx context.Context) (*dto.IDs, error) {
	_, resp, err := s.create.ServeGRPC(ctx, nil)
	if err != nil {
		return nil, err
	}
	return resp.(*dto.IDs), nil
}

// NewGRPCRoomService returns a valid implementation of RoomService wrapped by grpc.
func NewGRPCRoomService(_ context.Context, r room) *dto.RoomService {
	return &GRPCRoomService{
		create: grpc.NewServer(
			func(ctx context.Context, request interface{}) (interface{}, error) {
				return r.Create(ctx)
			},
			nil,
			nil,
		),
		listIDs: grpc.NewServer(
			func(ctx context.Context, request interface{}) (interface{}, error) {
				return r.ListIDs(ctx)
			},
			nil,
			nil,
		),
	}
}
