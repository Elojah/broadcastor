package redis

import "errors"

// Config is the config required for redis service.
type Config struct {
	Addr     string
	Password string
	DB       int
}

// Check check if config fields are valid.
func (c Config) Check() error {
	if c.Addr == "" {
		return errors.New("redis address url cannot be empty")
	}
	return nil
}
