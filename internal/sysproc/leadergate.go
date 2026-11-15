package sysproc

import (
	"os"
	"sync"
	"time"
)

// LeaderGate lets the group kill of a stop run only while the leader of the
// group is not reaped. Go runs cmd.Cancel from its own watch of the context.
// That call can come after the wait reaped the leader, and the number of an
// empty group is then free. The caller closes the gate after the exit of the
// leader and before the wait reaps it (CloseAfterExit). The zero value is an
// open gate.
type LeaderGate struct {
	mu     sync.Mutex
	closed bool
}

// Do runs kill while the gate is open. It holds the lock across kill, so the
// gate does not close and the reap does not start in between.
func (g *LeaderGate) Do(kill func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed {
		kill()
	}
}

// Close shuts the gate. After it, Do runs nothing.
func (g *LeaderGate) Close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

// CloseAfterExit closes the gate once p has exited. seen reports that a wait
// for the exit (AwaitExit) saw it. A wait that did not see it leaves the gate
// open while p surely runs (SurelyRuns), so a stop still sends its group kill.
// The poll ends when p exits or when the test of SurelyRuns fails (claustrum's
// own, not measured).
func (g *LeaderGate) CloseAfterExit(p *os.Process, seen bool) {
	for !seen && SurelyRuns(p) {
		time.Sleep(LeaderPollEvery)
	}
	g.Close()
}

// LeaderPollEvery is the time between two tests of a leader that the wait for
// its exit did not see (claustrum's own, not measured).
const LeaderPollEvery = 10 * time.Millisecond
