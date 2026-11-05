package main

import (
	"testing"
	"time"
)

// TestPruneExitedSkipsRunning is the deterministic half of the retention rule
// of TestSocketPrunesLongExitedProcesses (integration_test.go): no timers, no
// sockets, just the predicate. A process still marked running is kept
// however old its exitedAt looks, and one that exited inside the window is kept
// too — so a regression that prunes on age alone, or that ignores the window,
// fails here rather than showing up as a flake in that socket test.
func TestPruneExitedSkipsRunning(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.close)
	long := time.Now().Add(-time.Hour)

	m.procs["running-but-ancient"] = &managedProc{running: true, exitedAt: long}
	m.procs["exited-long-ago"] = &managedProc{running: false, exitedAt: long}
	m.procs["exited-just-now"] = &managedProc{running: false, exitedAt: time.Now()}
	m.procs["never-stamped"] = &managedProc{running: false}

	m.pruneExited()

	for _, id := range []string{"running-but-ancient", "exited-just-now", "never-stamped"} {
		if m.procs[id] == nil {
			t.Errorf("%s was pruned; it must be kept", id)
		}
	}
	if m.procs["exited-long-ago"] != nil {
		t.Errorf("exited-long-ago survived pruneExited")
	}
}
