package bc

// User represents a client user.
type User struct {
	ID ID
}

// UserMapper interfaces User data interactions.
type UserMapper interface {
	AddUser(User, ID, ID) error
	RemoveUser(User, ID) error
	GetUser(UserSubset) (User, error)
	ListUserIDs(UserSubset) ([]ID, uint64, error)
}

// UserSubset retrieves users based on room ID and cursor/count SCAN.
type UserSubset struct {
	ID     ID
	RoomID ID
	PoolID ID
	Cursor uint64
	Count  int64
}
