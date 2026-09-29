// Package gate provides Gate, which lets code run only while it is open.
package gate

import "sync"

// Gate starts open, and Close closes it for good. Close waits for every Enter to Leave, so code between them either
// finishes before Close returns or never starts. The zero value is open. It must not be copied after first use.
type Gate struct {
	mu     sync.RWMutex // read-held between Enter and Leave
	closed bool
}

// Enter reports whether g is open and, if so, keeps it open until Leave. An Enter during Close blocks, so between Enter
// and Leave, never Enter again nor wait on anything that waits on Close.
func (g *Gate) Enter() bool {
	g.mu.RLock()
	if g.closed {
		g.mu.RUnlock()

		return false
	}

	return true
}

// Leave ends an Enter that returned true.
func (g *Gate) Leave() {
	g.mu.RUnlock()
}

// Close closes g, waiting for every Leave, and reports whether it was already closed.
func (g *Gate) Close() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	closed := g.closed
	g.closed = true

	return closed
}
