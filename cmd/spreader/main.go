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

	// Message routes
	var m message
	m.UserMapper = rdx
	m.RoomMapper = rdx
	m.MessageMapper = rdx
	m.client = http.DefaultClient
	m.count = cfg.Count

	http.Handle("/message/send", httptransport.NewServer(
		m.MakeSendEndpoint(),
		m.DecodeReq,
		m.EncodeResp,
	))

	log.Fatal(http.ListenAndServe(cfg.Address, nil))
}

func main() {

	var filepath string
	flag.StringVar(&filepath, "c", "", "configuration file in JSON")

	flag.Parse()

	run(filepath)

}
