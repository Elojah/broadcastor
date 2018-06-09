package redis

import (
	"github.com/oklog/ulid"

	bc "github.com/elojah/broadcastor"
)

const (
	userkey = "user:"
)

// AddUser implements UserMapper with redis.
func (s *Service) AddUser(user bc.User, roomID bc.ID) error {
	return s.Set(
		userkey+roomID.String()+":"+user.ID.String(),
		"",
		0,
	).Err()
}

// RemoveUser implements UserMapper with redis.
func (s *Service) RemoveUser(user bc.User, roomID bc.ID) error {
	return s.Del(
		userkey + roomID.String() + ":" + user.ID.String(),
	).Err()
}

// ListUsers implements UserMapper with redis.
func (s *Service) ListUsers(subset bc.UserSubset) ([]bc.ID, uint64, error) {
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
		users[i] = ulid.MustParse(key)
	}
	return users, cursor, nil
}
