package bc

// Room represents a group of users in the same room.
type Room struct {
	ID    ID
	Pools []ID
}

// RoomMapper interfaces data room interactions.
type RoomMapper interface {
	CreateRoom(Room) error
	GetRoom(RoomSubset) (Room, error)
	ListRoomIDs() ([]ID, error)
}

// RoomSubset retrieves one room per ID.
type RoomSubset struct {
	ID ID
}
