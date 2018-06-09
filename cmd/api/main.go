package main

import (
	"context"
	"errors"
	"flag"
	"net"

	log "github.com/sirupsen/logrus"

	"github.com/elojah/broadcastor/storage/redis"
)

func run(filepath string) {

	cfg, err := NewConfig(filepath)
	if err != nil {
		log.WithField("read", "config").Error(err)
		return
	}
	if err := cfg.Check(); err != nil {
		log.WithField("check", "config").Error(err)
		return
	}
	rdx := redis.NewService()
	if err := rdx.Dial(cfg.Redis); err != nil {
		log.WithField("dial", "redis").Error(err)
		return
	}

	var r room
	r.RoomMapper = rdx

	listener, err := net.Listen("tcp", ":9090")
	if err != nil {
		log.WithField("port", ":9090").Error(errors.New("failed to listen port"))
		return
	}
	rms := NewGRPCRoomService(context.Background(), r)
	if err := rms.Listen(listener); err != nil {
		log.Error(errors.New("failed to serve room service"))
		return
	}

	select {}
}

func main() {

	var filepath string
	flag.StringVar(&filepath, "c", "", "configuration file in JSON")

	flag.Parse()

	run(filepath)

}
