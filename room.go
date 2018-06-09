package bc

// Room represents a group of users in the same room.
type Room struct {
	ID ID
}

// RoomMapper interfaces data room interactions.
type RoomMapper interface {
	CreateRoom(Room) error
	ListRoomIDs() ([]ID, error)
}
