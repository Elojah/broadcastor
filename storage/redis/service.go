package redis

import (
	"github.com/go-redis/redis"

	bc "github.com/elojah/broadcastor"
)

var _ bc.MessageMapper = (*Service)(nil)
var _ bc.RoomMapper = (*Service)(nil)
var _ bc.UserMapper = (*Service)(nil)

// Service is a mem service to store data directly in memory.
type Service struct {
	*redis.Client
}

// NewService returns a new valid service.
func NewService() *Service {
	return &Service{}
}

// Close closes the redis service.
func (s *Service) Close() {
	s.Close()
}

// Dial initializes the redis service based on config.
func (s *Service) Dial(cfg Config) error {
	s.Client = redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	_, err := s.Client.Ping().Result()
	return err
}
