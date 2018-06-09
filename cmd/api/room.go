package main

import (
	"context"

	grpc "github.com/go-kit/kit/transport/grpc"

	bc "github.com/elojah/broadcastor"
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
	return r.CreateRoom(bc.Room{ID: bc.NewID()})
}

func (r room) ListIDs(_ context.Context) ([]bc.ID, error) {
	return r.ListRoomIDs()
}

func makeCreateRoom(svc RoomService) endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		return svc.Create(ctx)
	}
}

func makeListIDsRoom(svc RoomService) endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		return svc.ListIDs(ctx)
	}
}

type GRPCRoomService struct {
	create  grpc.Handler
	listIDs grpc.Handler
}

func (s *GRPCRoomService) Create(ctx context.Context, room *dto.Room) (*dto.Error, error) {
	_, resp, err := s.listIDs.ServeGRPC(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.(*pb.RoomResponse), nil
}

func (s *GRPCRoomService) ListIDs(ctx context.Context, req *pb.ListIDsRoomRequest) (*pb.RoomResponse, error) {
	_, resp, err := s.create.ServeGRPC(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.(*pb.RoomResponse), nil
}

func NewGRPCServer(_ context.Context, endpoint Endpoints) pb.RoomServer {
	return &GRPCRoomService{
		create: grpc.NewServer(
			endpoint.CreateRoomEndpoint,
			DecodeCreateRoomRequest,
			EncodeRoomResponse,
		),
		listIDs: grpc.NewServer(
			endpoint.ListIDsRoomEndpoint,
			DecodeListIDsRoomRequest,
			EncodeRoomResponse,
		),
	}
}
