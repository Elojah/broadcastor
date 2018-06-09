package main

import (
	"flag"

	grpc "github.com/go-kit/kit/transport/grpc"
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

	r := room
	r.RoomMapper = rdx
	grpc.Foo()

	select {}
}

func main() {

	var filepath string
	flag.StringVar(&filepath, "c", "", "configuration file in JSON")

	flag.Parse()

	run(filepath)

}
