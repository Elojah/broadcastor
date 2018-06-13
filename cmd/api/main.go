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
		log.WithField("read", "config").WithField("file", filepath).Error(err)
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

	// Room routes
	var r room
	r.pools = cfg.NPools
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

	// User routes
	var u user
	u.UserMapper = rdx
	u.RoomMapper = rdx
	u.clientPort = cfg.ClientPort

	http.Handle("/user/create", httptransport.NewServer(
		u.MakeCreateEndpoint(),
		u.DecodeReq,
		u.EncodeResp,
	))

	// Message routes
	var m message
	m.UserMapper = rdx
	m.MessageMapper = rdx
	m.RoomMapper = rdx
	m.spreaders = make([]string, len(cfg.SpreaderAddresses))
	copy(m.spreaders, cfg.SpreaderAddresses)
	m.client = http.DefaultClient

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
