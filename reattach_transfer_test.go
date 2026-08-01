package main

import (
	"testing"
	"time"
)

// process.reattach TRANSFERS the frame stream to the reattaching connection; it
// does not add a second listener. Measured at 5db5e4a: output produced after a
// reattach reaches only the new connection, while the previously attached one
// stops receiving. claustrum fanned out to both, so a resumed session
// double-delivered every frame to whichever old connection was still open — and
// because the new connection also gets a replay, those duplicates overlapped.
func TestReattachTransfersTheFrameStream(t *testing.T) {
	m := newTestProcManager(t)

	old, oldFrames := pipeConn(t)
	// The test binary in "sleep" mode, not /bin/sh: AGENTS.md requires process
	// fixtures to come from the test binary, and a /bin/sh fixture cannot run on
	// the Windows CI leg at all.
	exe, env := helperCommand(t, "sleep")
	if _, err := m.spawn(old, "xfer", exe, []string{"30"}, "", env); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	p := m.get("xfer")
	if p == nil {
		t.Fatal("spawned process not found")
	}

	fresh, freshFrames := pipeConn(t)
	if _, found, _, _, _, _ := m.reattach(fresh, "xfer", 0); !found {
		t.Fatal("reattach did not find the live process")
	}

	// Drain anything buffered from before the reattach so the next frame is
	// unambiguously post-reattach.
	drain(oldFrames)
	drain(freshFrames)

	p.emit(streamFrame{Stream: "stdout", Data: "postreattach"})

	select {
	case <-freshFrames:
	case <-time.After(2 * time.Second):
		t.Error("the reattaching connection did not receive the frame")
	}
	select {
	case f := <-oldFrames:
		t.Errorf("the previously attached connection still received %+v — reattach "+
			"fanned out instead of transferring", f)
	case <-time.After(300 * time.Millisecond):
	}
}

// drain empties a frame channel without blocking.
func drain(ch <-chan streamFrame) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
