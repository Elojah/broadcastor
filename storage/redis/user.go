package redis

import (
	"encoding/json"
	"strings"

	"github.com/oklog/ulid"

	bc "github.com/elojah/broadcastor"
)

const (
	userkey = "user:"
)

// AddUser implements UserMapper with redis.
func (s *Service) AddUser(user bc.User, roomID bc.ID) error {
	raw, err := json.Marshal(user)
	if err != nil {
		return err
	}
	return s.Set(
		userkey+roomID.String()+":"+user.ID.String(),
		raw,
		0,
	).Err()
}

// RemoveUser implements UserMapper with redis.
func (s *Service) RemoveUser(user bc.User, roomID bc.ID) error {
	return s.Del(
		userkey + roomID.String() + ":" + user.ID.String(),
	).Err()
}

// ListUserIDs implements UserMapper with redis.
func (s *Service) ListUserIDs(subset bc.UserSubset) ([]bc.ID, uint64, error) {
	keys, cursor, err := s.Scan(
		subset.Cursor,
		userkey+subset.RoomID.String(),
		subset.Count,
	).Result()
	if err != nil {
		return nil, 0, err
	}
	users := make([]bc.ID, len(keys))
	for i, key := range keys {
		users[i] = ulid.MustParse(strings.Split(key, ":")[2])
	}
	return users, cursor, nil
}
