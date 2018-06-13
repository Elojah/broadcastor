package redis

import (
	"encoding/json"

	bc "github.com/elojah/broadcastor"
)

const (
	messagekey = "message:"
)

// CreateMessage implements MessageMapper with redis.
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

// GetMessage implements MessageMapper with redis.
func (s *Service) GetMessage(subset bc.MessageSubset) (bc.Message, error) {
	val, err := s.Get(
		messagekey + subset.ID.String(),
	).Result()
	if err != nil {
		return bc.Message{}, err
	}
	var msg bc.Message
	err = json.Unmarshal([]byte(val), &msg)
	return msg, err
}
