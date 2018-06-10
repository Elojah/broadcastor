package main

import (
	"flag"
	"net/http"

	httptransport "github.com/go-kit/kit/transport/http"
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

	http.Handle("/room/create", httptransport.NewServer(
		r.MakeCreateEndpoint(),
		r.DecodeReq,
		r.EncodeResp,
	))

	http.Handle("/room/list", httptransport.NewServer(
		r.MakeListIDsEndpoint(),
		r.DecodeReq,
		r.EncodeResp,
	))

	log.Fatal(http.ListenAndServe(cfg.Address, nil))
}

func main() {

	var filepath string
	flag.StringVar(&filepath, "c", "", "configuration file in JSON")

	flag.Parse()

	run(filepath)

}
