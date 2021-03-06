package bc

import (
	"crypto/rand"
	"time"

	"github.com/oklog/ulid"
)

// ID is an alias of ulid.ULID.
type ID = ulid.ULID

// NewID returns a new random ID.
func NewID() ID {
	return ID(ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader))
}
