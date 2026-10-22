package main

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestReattachNewConnGetsNoLiveFrameBeforeReplayEnds pins cell R1 of rounds
// tier3-03 and tier3-12 (5fd08069, macOS and Linux VMs). The seam starts a live
// emit after p.mu goes and before the replay is written. That frame goes out after
// the replay, and none is lost. The wait in the seam is a bound for a write that
// does not come: the emit blocks on the write lock of the connection.
func TestReattachNewConnGetsNoLiveFrameBeforeReplayEnds(t *testing.T) {
	m := newTestProcManager(t)
	p := &managedProc{id: "r1", subs: map[*conn]struct{}{}, running: true}
	for range 3 {
		p.emit(streamFrame{Stream: "stdout", Data: "YQ=="}) // seq 1 to 3
	}
	m.procs["r1"] = p

	old := afterReplaySnapshot
	t.Cleanup(func() { afterReplaySnapshot = old })
	emitted := make(chan struct{})
	afterReplaySnapshot = func(string) {
		afterReplaySnapshot = old
		go func() {
			p.emit(streamFrame{Stream: "stdout", Data: "Yg=="}) // seq 4, live
			close(emitted)
		}()
		select {
		case <-emitted:
		case <-time.After(200 * time.Millisecond):
		}
	}

	c, frames := pipeConn(t)
	_, found, _, _, last, _ := m.reattach(c, "r1", 0)
	if !found || last != 3 {
		t.Fatalf("reattach found %v, lastSeq %d, want true and 3", found, last)
	}
	<-emitted
	p.emit(streamFrame{Stream: "stdout", Data: "Yw=="}) // seq 5, after the reattach
	got := seqs(collect(t, frames, 5))
	want := []uint64{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("seqs on the new connection = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("seqs on the new connection = %v, want %v", got, want)
		}
	}
}

// A failed replay write on a new connection releases its write lock, detaches it
// and logs the replay line.
func TestReattachNewConnReplayWriteErrorDetaches(t *testing.T) {
	buf := captureLogBuf(t)
	m := newTestProcManager(t)
	p := &managedProc{id: "r3", subs: map[*conn]struct{}{}, running: true}
	p.emit(streamFrame{Stream: "stdout", Data: "YQ=="}) // seq 1
	m.procs["r3"] = p

	c, _ := pipeConn(t)
	_ = c.nc.Close()
	m.reattach(c, "r3", 0)

	p.mu.Lock()
	_, on := p.subs[c]
	p.mu.Unlock()
	if on {
		t.Error("a connection whose replay write failed is still attached")
	}
	if !c.wmu.TryLock() {
		t.Fatal("the write lock of the connection is still held after the reattach")
	}
	c.wmu.Unlock()
	if !strings.Contains(buf.String(), "[frameSink] replay write failed, detaching") {
		t.Errorf("log %q, want the replay write failed line", buf.String())
	}
}

// TestReattachLargeReplayToSlowReaderAnswers holds a producer that emits as fast
// as it can, a reader that is slow, and a large replay. The reattach answers in a
// bounded time, the stored frames come first, and the seq on the wire never goes
// down. A hold that kept live lines for the replay did not answer within 20 s
// here, in an earlier draft of this fix.
func TestReattachLargeReplayToSlowReaderAnswers(t *testing.T) {
	m := newTestProcManager(t)
	p := &managedProc{id: "big", subs: map[*conn]struct{}{}, running: true}
	data := strings.Repeat("QUFB", 256) // 1 KiB of base64 per frame
	const stored = 2000
	for range stored {
		p.emit(streamFrame{Stream: "stdout", Data: data})
	}
	m.procs["big"] = p

	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	c := &conn{nc: client}
	var mu sync.Mutex
	var got []uint64
	go func() {
		sc := bufio.NewScanner(server)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			var f streamFrame
			if json.Unmarshal(sc.Bytes(), &f) == nil {
				mu.Lock()
				got = append(got, f.Seq)
				mu.Unlock()
			}
			time.Sleep(50 * time.Microsecond) // the slow reader
		}
	}()

	stop := make(chan struct{})
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for {
			select {
			case <-stop:
				return
			default:
				p.emit(streamFrame{Stream: "stdout", Data: data})
			}
		}
	}()
	t.Cleanup(func() { close(stop); client.Close(); <-producerDone })

	answered := make(chan uint64, 1)
	go func() {
		_, _, _, _, last, _ := m.reattach(c, "big", 0)
		answered <- last
	}()
	var last uint64
	select {
	case last = <-answered:
	case <-time.After(20 * time.Second):
		t.Fatal("the reattach did not answer within 20 s")
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > int(last)+10 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) < int(last) {
		t.Fatalf("got %d frames, want at least the %d stored ones", len(got), last)
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1]+1 {
			t.Fatalf("seq %d follows seq %d on the wire, want %d", got[i], got[i-1], got[i-1]+1)
		}
	}
}

// waitHeld polls up to 1 s for mu to be held by another goroutine. It is a
// bound for a state that the fixed code never reaches, so it does not fail. The
// bound is long so that a slow runner still reaches that state on the old code.
func waitHeld(mu *sync.Mutex) {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !mu.TryLock() {
			return
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
	}
}

// within fails the test unless each function returns within 3 s.
func within(t *testing.T, what string, fns ...func()) {
	t.Helper()
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func() { defer wg.Done(); fn() }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not return within 3 s", what)
	}
}

// A reattach that waits for the write lock of a connection whose writer is stuck
// holds no lock of the process. The prune, a lookup and a process.stdin of the
// process still return.
func TestReattachWaitingForAStuckWriterHoldsNoProcessLock(t *testing.T) {
	m := newTestProcManager(t)
	p := &managedProc{id: "w1", subs: map[*conn]struct{}{}, running: true}
	p.stdinCond = sync.NewCond(&p.stdinMu)
	p.emit(streamFrame{Stream: "stdout", Data: "YQ=="})
	m.procs["w1"] = p
	m.procs["other"] = &managedProc{id: "other", subs: map[*conn]struct{}{}, running: true}

	c, _ := pipeConn(t)
	c.wmu.Lock() // a writer stuck on a full socket
	released := false
	t.Cleanup(func() {
		if !released {
			c.wmu.Unlock()
		}
	})
	reattached := make(chan struct{})
	go func() { m.reattach(c, "w1", 0); close(reattached) }()
	waitHeld(&p.mu)

	within(t, "the prune, the lookup and process.stdin",
		m.pruneExited,
		func() { _ = m.get("other") },
		func() { p.applyStdin([]byte("x"), nil, p.isLive) },
	)
	released = true
	c.wmu.Unlock()
	select {
	case <-reattached:
	case <-time.After(3 * time.Second):
		t.Fatal("the reattach did not return after the writer ended")
	}
}

// The cycle of a write lock, the stdin lock and the process lock. Reattach R of
// a new connection is in its replay. Reattach D from another connection runs to
// the end, so a second reattach R2 on the first connection is a new one again.
// A process.stdin S comes next. R, R2 and S all return.
func TestReattachPipelinedOnTheSameConnDoesNotDeadlock(t *testing.T) {
	m := newTestProcManager(t)
	p := &managedProc{id: "w2", subs: map[*conn]struct{}{}, running: true}
	p.stdinCond = sync.NewCond(&p.stdinMu)
	p.emit(streamFrame{Stream: "stdout", Data: "YQ=="})
	m.procs["w2"] = p

	c, _ := pipeConn(t)
	d, _ := pipeConn(t)
	r2Done := make(chan struct{})
	sDone := make(chan struct{})
	old := afterReplaySnapshot
	t.Cleanup(func() { afterReplaySnapshot = old })
	afterReplaySnapshot = func(string) {
		afterReplaySnapshot = old
		m.reattach(d, "w2", 0) // D, to the end
		go func() { m.reattach(c, "w2", 0); close(r2Done) }()
		waitHeld(&p.mu)
		go func() { p.applyStdin([]byte("x"), nil, p.isLive); close(sDone) }()
		waitHeld(&p.stdinMu)
	}
	within(t, "reattach R, reattach R2 and process.stdin S",
		func() { m.reattach(c, "w2", 0) },
		func() { <-r2Done },
		func() { <-sDone },
	)
}
