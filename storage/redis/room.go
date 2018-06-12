package redis

import (
	"encoding/json"
	"strings"

	"github.com/oklog/ulid"

	bc "github.com/elojah/broadcastor"
)

const (
	roomkey = "room:"
)

// CreateRoom implements RoomMapper with redis.
func (s *Service) CreateRoom(room bc.Room) error {
	raw, err := json.Marshal(room)
	if err != nil {
		return err
	}
	return s.Set(
		roomkey+room.ID.String(),
		string(raw),
		0,
	).Err()
}

// GetRoom implements RoomMapper with redis.
func (s *Service) GetRoom(subset bc.RoomSubset) (bc.Room, error) {
	val, err := s.Get(
		roomkey + subset.ID.String(),
	).Result()
	var room bc.Room
	err = json.Unmarshal([]byte(val), &room)
	return room, err
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
