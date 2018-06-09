package bc

// Message represents a sent message by a client to API.
type Message struct {
	ID      ID
	UserID  ID
	RoomID  ID
	Content string
}
