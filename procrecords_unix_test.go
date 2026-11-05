//go:build linux || darwin

package main

import (
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/childrec"
)

// TestAwaitRecordsRemovedIsBounded pins the bound of the shutdown wait: a child whose
// exit goroutine never reports does not hold the daemon past shutdownRecordWait.
func TestAwaitRecordsRemovedIsBounded(t *testing.T) {
	old := childrec.SetShutdownRecordWaitForTest(50 * time.Millisecond)
	t.Cleanup(func() { childrec.SetShutdownRecordWaitForTest(old) })
	m := newTestProcManager(t)
	m.RunDir = t.TempDir()
	m.procs["stuck"] = &managedProc{id: "stuck", recordDone: make(chan struct{})}
	m.procs["bare"] = &managedProc{id: "bare"} // no channel: nothing to wait for
	done := make(chan struct{})
	go func() { defer close(done); m.awaitRecordsRemoved() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the shutdown wait did not end at its bound")
	}
}
