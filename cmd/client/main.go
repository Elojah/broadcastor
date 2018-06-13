package main

import (
	"flag"
	"net/http"

	httptransport "github.com/go-kit/kit/transport/http"
	log "github.com/sirupsen/logrus"
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

	var m message
	http.Handle("/message/receive", httptransport.NewServer(
		m.MakeReceiveEndpoint(),
		m.DecodeReq,
		m.EncodeResp,
	))

	go func() { log.Fatal(http.ListenAndServe(cfg.ClientAddress, nil)) }()

	var c console
	c.serverAddr = cfg.ServerAddress
	c.client = http.DefaultClient
	c.read()
}

func main() {

	var filepath string
	flag.StringVar(&filepath, "c", "", "configuration file in JSON")

	flag.Parse()

	run(filepath)

}
