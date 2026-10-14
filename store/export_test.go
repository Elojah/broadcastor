package store

import "os"

// SetFsync replaces how f syncs a file, so that tests count syncs or fail them.
func SetFsync[T any](f *File[T], fsync func(*os.File) error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.fsync = fsync
}
