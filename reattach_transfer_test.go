package main

import (
	"sync"
	"sync/atomic"
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

// The exclusive transfer must hold under a CONCURRENT emit, not just a
// sequential one. Raised on review: if emit snapshots the subscriber set and a
// reattach replaces p.subs before the write lands, does the old connection get a
// post-transfer frame?
//
// It cannot, and the reason is the lock structure rather than luck. emit stamps
// f.Seq, appends to p.buffer, and copies p.subs inside ONE p.mu critical
// section, and reattach swaps p.subs under the same lock. So the two serialize,
// and every frame falls on one side of the cut:
//
//	emit locks first     -> subs is the OLD set, and seq <= the lastSeq the
//	                        reattach then reports; the frame is also already in
//	                        p.buffer, so the new connection gets it in the replay
//	emit locks second    -> subs is ALREADY {new}, so the old connection is gone
//
// The assertion is therefore the seq-precise one — the old connection never sees
// a seq ABOVE the transfer point — not "the old connection sees nothing more",
// which would be wrong for a frame that legitimately predates the cut.
//
// SENSITIVITY, verified rather than assumed: this test passes against a mutant
// that merely splits the critical section, because the resulting window is a few
// nanoseconds and 300 rounds never land in it. Widening that mutant's window to
// 50us makes it fail immediately ("OLD conn received seq 2 > lastSeq 1"), which
// is what shows the assertion can detect a real leak at all. Treat it as a guard
// against a future refactor that widens the window, not as proof on its own.
func TestReattachTransferHoldsUnderConcurrentEmit(t *testing.T) {
	for round := 0; round < 300; round++ {
		oldConn, oldFrames := pipeConn(t)
		fresh, _ := pipeConn(t)
		p := &managedProc{id: "r", subs: map[*conn]struct{}{oldConn: {}}}

		var stop atomic.Bool
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				p.emit(streamFrame{Stream: "stdout", Data: "eA=="})
			}
		}()
		time.Sleep(200 * time.Microsecond) // let the emitter get going

		// The transfer, exactly as reattach performs it.
		p.mu.Lock()
		lastSeq := p.seq
		p.subs = map[*conn]struct{}{fresh: {}}
		p.mu.Unlock()

		stop.Store(true)
		wg.Wait()

		for drained := false; !drained; {
			select {
			case f := <-oldFrames:
				if f.Seq > lastSeq {
					t.Fatalf("round %d: the old connection received seq %d, above the "+
						"transfer point lastSeq=%d", round, f.Seq, lastSeq)
				}
			default:
				drained = true
			}
		}
	}
}
