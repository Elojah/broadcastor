package main

import (
	"encoding/json"
	"io/ioutil"

	"github.com/elojah/broadcastor/storage/redis"
)

type Config struct {
	Redis redis.Config
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
	return c.Redis.Check()
}
