// Package gate provides Gate, which starts open and closes once, and which lets code run only while it is open: such
// code either runs entirely before Close returns, or not at all.
package gate

import "sync"

// Gate starts open and is closed by the first call to Close. Every Enter either returns false, once Close has been
// called, or returns true and keeps g open until the matching Leave, which Close waits for. Its zero value is an open
// Gate. It must not be copied after first use.
type Gate struct {
	// mu is held for reading between Enter and Leave, and for writing while Close sets closed.
	mu     sync.RWMutex
	closed bool
}

// Enter reports whether g is open and, if it is, keeps it open until Leave is called. Every Enter that returns true
// must be followed by exactly one Leave, and one that returns false by none. Close waits for Leave, and an Enter that
// comes while Close waits blocks until Close is done, so between Enter and Leave the caller must neither Enter g again
// nor wait on anything that waits on Close.
func (g *Gate) Enter() bool {
	g.mu.RLock()
	if g.closed {
		g.mu.RUnlock()

		return false
	}

	return true
}

// Leave lets g close again, after an Enter that returned true.
func (g *Gate) Leave() {
	g.mu.RUnlock()
}

// Close closes g and reports whether it was already closed. It waits for every Enter that returned true to Leave, so
// once it returns, nothing is between Enter and Leave and every later Enter returns false.
func (g *Gate) Close() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	closed := g.closed
	g.closed = true

	return closed
}
