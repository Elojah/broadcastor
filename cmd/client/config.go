package main

import (
	"encoding/json"
	"errors"
	"io/ioutil"
)

// Config represents client config required.
type Config struct {
	ServerAddress string `json:"server_address"`
	ClientAddress string `json:"client_address"`
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
	if c.ServerAddress == "" {
		return errors.New("missing server address")
	}
	if c.ClientAddress == "" {
		return errors.New("missing client address")
	}
	return nil
}
