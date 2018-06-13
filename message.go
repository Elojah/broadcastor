package bc

// Message represents a sent message by a client to API.
type Message struct {
	ID      ID
	UserID  ID
	RoomID  ID
	Content string
}

// MessageMapper interfaces Message data interactions.
type MessageMapper interface {
	CreateMessage(Message) error
	GetMessage(MessageSubset) (Message, error)
}

// MessageSubset retrieves a message per ID.
type MessageSubset struct {
	ID ID
}

// MessageRequest is a request sent to spreaders to specify which messages to retrieve.
type MessageRequest struct {
	MessageID ID
	RoomID    ID
	PoolID    ID
}
