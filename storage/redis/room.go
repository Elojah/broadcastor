package redis

import (
	"strings"

	"github.com/oklog/ulid"

	bc "github.com/elojah/broadcastor"
)

const (
	roomkey = "room:"
)

// CreateRoom implements RoomMapper with redis.
func (s *Service) CreateRoom(room bc.Room) error {
	return s.Set(
		roomkey+room.ID.String(),
		"",
		0,
	).Err()
}

// ListRoomIDs implements RoomMapper with redis.
func (s *Service) ListRoomIDs() ([]bc.ID, error) {
	keys, err := s.Keys(
		roomkey + "*",
	).Result()
	if err != nil {
		return nil, err
	}
	rooms := make([]bc.ID, len(keys))
	for i, key := range keys {
		rooms[i] = ulid.MustParse(strings.Split(key, ":")[1])
	}
	return rooms, nil
}
