package bc

import (
	"errors"
)

var (
	// ErrNotFound is raised when a required resource doesn't exist.
	ErrNotFound = errors.New("resource not found")
)
