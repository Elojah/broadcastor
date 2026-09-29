package gate_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/elojah/broadcastor/pkg/gate"
)

// deadlockTimeout bounds every wait, so a deadlock fails the test instead of hanging. These tests run in real time: a
// goroutine blocked on a sync.RWMutex would hang a synctest bubble.
const deadlockTimeout = 10 * time.Second

func TestGate_Close(t *testing.T) {
	t.Parallel()

	var g gate.Gate
	if !g.Enter() {
		t.Fatal("Enter on an open gate returned false")
	}
	g.Leave()

	if g.Close() {
		t.Error("first Close reported the gate already closed")
	}
	if !g.Close() {
		t.Error("second Close reported the gate open")
	}
	if g.Enter() {
		t.Error("Enter on a closed gate returned true")
	}
}

// Close does not return while anyone is between Enter and Leave. Waiting a little can only miss a Close that returns
// too early, never fail a correct one.
func TestGate_CloseWaitsForLeave(t *testing.T) {
	t.Parallel()

	var g gate.Gate
	if !g.Enter() {
		t.Fatal("Enter on an open gate returned false")
	}

	closed := make(chan struct{})
	go func() {
		g.Close()
		close(closed)
	}()

	select {
	case <-closed:
		t.Fatal("Close returned before Leave")
	case <-time.After(10 * time.Millisecond):
	}

	g.Leave()
	select {
	case <-closed:
	case <-time.After(deadlockTimeout):
		t.Fatal("Close did not return after Leave")
	}
}

// Whatever runs between a successful Enter and its Leave has run once Close returns, and nothing runs after.
func TestGate_Concurrent(t *testing.T) {
	t.Parallel()

	var g gate.Gate
	var n atomic.Int64
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for g.Enter() {
				n.Add(1)
				g.Leave()
			}
		})
	}

	g.Close()
	want := n.Load()
	wg.Wait()

	if got := n.Load(); got != want {
		t.Errorf("counted %d once Close returned, then %d", want, got)
	}
}
