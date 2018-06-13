package redis

import (
	"encoding/json"
	"strings"

	bc "github.com/elojah/broadcastor"
)

const (
	userkey = "user:"
)

// AddUser implements UserMapper with redis.
func (s *Service) AddUser(user bc.User, roomID bc.ID, poolID bc.ID) error {
	raw, err := json.Marshal(user)
	if err != nil {
		return err
	}
	return s.Set(
		userkey+roomID.String()+":"+poolID.String()+":"+user.ID.String()+":"+user.Addr,
		raw,
		0,
	).Err()
}

// GetUser implements UserMapper with redis.
func (s *Service) GetUser(subset bc.UserSubset) (bc.User, error) {
	keys, err := s.Keys(
		userkey + subset.RoomID.String() + ":" + subset.PoolID.String() + ":" + subset.ID.String() + ":*",
	).Result()
	if err != nil {
		return bc.User{}, err
	}
	if len(keys) == 0 {
		return bc.User{}, bc.ErrNotFound
	}
	val, err := s.Get(keys[0]).Result()
	if err != nil {
		return bc.User{}, err
	}
	var user bc.User
	err = json.Unmarshal([]byte(val), &user)
	return user, err
}

// RemoveUser implements UserMapper with redis.
func (s *Service) RemoveUser(user bc.User, roomID bc.ID) error {
	keys, err := s.Keys(
		userkey + roomID.String() + ":*:" + user.ID.String() + ":*",
	).Result()
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := s.Del(key).Err(); err != nil {
			return err
		}
	}
	return nil
}

// ListUserAddr implements UserMapper with redis.
func (s *Service) ListUserAddr(subset bc.UserSubset) ([]string, uint64, error) {
	keys, cursor, err := s.Scan(
		subset.Cursor,
		userkey+subset.RoomID.String()+":"+subset.PoolID.String()+":*",
		subset.Count,
	).Result()
	if err != nil {
		return nil, 0, err
	}
	addrs := make([]string, len(keys))
	for i, key := range keys {
		addrs[i] = strings.Split(key, ":")[4]
	}
	return addrs, cursor, nil
}
