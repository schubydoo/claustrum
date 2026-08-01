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
// sequential one. Raised on review of #202: if emit snapshots the subscriber set
// and a reattach replaces p.subs before the write lands, does the old connection
// get a post-transfer frame?
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
// THE FRAMES MUST BE DRAINED WHILE THE EMITTER RUNS. pipeConn's channel holds 64
// frames; once it fills, the scanner stops reading, the net.Pipe write blocks
// inside emit, and the emitter never re-checks its stop flag — the test then
// hangs forever rather than failing. The first version of this test drained only
// after stopping the emitter and timed out the macOS leg at 10 minutes after
// passing on linux and windows, purely because that runner scheduled ~65 frames
// into a 64-slot buffer where this host produced 18.
//
// SENSITIVITY, verified rather than assumed: this test passes against a mutant
// that merely splits emit's critical section, because the resulting window is a
// few nanoseconds and no realistic round count lands in it. Widening that
// mutant's window to 50us makes it fail at once ("OLD conn received seq 2 >
// lastSeq 1"). Treat it as a guard against a refactor that widens the window,
// not as the proof — the proof is the single critical section.
func TestReattachTransferHoldsUnderConcurrentEmit(t *testing.T) {
	for round := 0; round < 100; round++ {
		oldConn, oldFrames := pipeConn(t)
		fresh, freshFrames := pipeConn(t)
		p := &managedProc{id: "r", subs: map[*conn]struct{}{oldConn: {}}}

		// Drain both sides continuously so no write can ever block.
		var mu sync.Mutex
		var oldSeqs []uint64
		drain := make(chan struct{})
		var drainers sync.WaitGroup
		drainers.Add(2)
		go func() {
			defer drainers.Done()
			for {
				select {
				case f := <-oldFrames:
					mu.Lock()
					oldSeqs = append(oldSeqs, f.Seq)
					mu.Unlock()
				case <-drain:
					return
				}
			}
		}()
		go func() {
			defer drainers.Done()
			for {
				select {
				case <-freshFrames:
				case <-drain:
					return
				}
			}
		}()

		var stop atomic.Bool
		var emitter sync.WaitGroup
		emitter.Add(1)
		go func() {
			defer emitter.Done()
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
		emitter.Wait()
		time.Sleep(2 * time.Millisecond) // let in-flight writes land
		close(drain)
		drainers.Wait()

		mu.Lock()
		got := append([]uint64(nil), oldSeqs...)
		mu.Unlock()
		for _, seq := range got {
			if seq > lastSeq {
				t.Fatalf("round %d: the old connection received seq %d, above the "+
					"transfer point lastSeq=%d", round, seq, lastSeq)
			}
		}
	}
}
