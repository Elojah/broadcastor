package main

import (
	"encoding/json"
	"errors"
	"io/ioutil"

	"github.com/elojah/broadcastor/storage/redis"
)

// Config is the configuration object for API service.
type Config struct {
	Address           string
	NPools            uint
	SpreaderAddresses []string
	Redis             redis.Config
}

// NewConfig creates a new Config initialized from filepath in JSON format.
func NewConfig(filepath string) (Config, error) {
	raw, err := ioutil.ReadFile(filepath)
	if err != nil {
		return Config{}, err
	}

	var c Config
	err = json.Unmarshal(raw, &c)
	return c, err
}

// Check check if Config fields are valid.
func (c Config) Check() error {
	if c.Address == "" {
		return errors.New("missing api address")
	}
	if c.NPools == 0 {
		return errors.New("missing pool number per room")
	}
	if len(c.SpreaderAddresses) == 0 {
		return errors.New("missing spreader addresses")
	}
	return c.Redis.Check()
}
