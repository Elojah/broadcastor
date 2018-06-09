package redis

import (
	"encoding/json"

	bc "github.com/elojah/broadcastor"
)

const (
	messagekey = "message:"
)

// AddMessage implements MessageMapper with redis.
func (s *Service) CreateMessage(message bc.Message) error {
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return s.Set(
		messagekey+message.ID.String(),
		raw,
		0,
	).Err()
}
