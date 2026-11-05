package main

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/supervisor"
)

// TestLinkBoundGoneAtSpawn: a link whose connection is already gone makes the
// process detached at once, so it ends after its unattended time with no
// detachConn. The end line is the one of 683cc6a9 (measured on Linux).
func TestLinkBoundGoneAtSpawn(t *testing.T) {
	buf := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	exe, env := helperCommand(t, "sleep")
	p, err := m.spawnVia(closedConn(conn.New(nil, nil, 0, nil, nil, nil)), "gone1", exe, []string{"60"}, "", env, false, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the process of a gone link did not end")
	}
	logs := buf.String()
	if !strings.Contains(logs, "[process.Manager] Process gone1: no connection attached for 1s; ending it") {
		t.Errorf("no ending line in:\n%s", logs)
	}
	if !strings.Contains(logs, "Process gone1 exited with code") || !strings.Contains(logs, ", asked to end by this daemon (unattended)") {
		t.Errorf("no unattended exit line in:\n%s", logs)
	}
	if m.get("gone1") != nil {
		t.Error("the record of the link-bound process stayed after its exit")
	}
}

// TestLinkBoundAttachedLinkStays: a process whose link is there gets no end.
func TestLinkBoundAttachedLinkStays(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	exe, env := helperCommand(t, "sleep")
	c, _ := pipeConn(t)
	p, err := m.spawnVia(c, "here1", exe, []string{"60"}, "", env, false, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
		t.Fatal("the process of an attached link ended")
	case <-time.After(1500 * time.Millisecond):
	}
	if p.bufCap != linkReplayCap {
		t.Errorf("bufCap = %d, want %d", p.bufCap, linkReplayCap)
	}
	// A spawn of the id of a running link-bound process is refused too.
	if _, err := m.spawnVia(nil, "here1", exe, []string{"0"}, "", env, false, nil, 0); err != errSpentID {
		t.Errorf("spawn over a running link-bound process = %v, want the spent-id refusal", err)
	}
}

// TestLinkReplayCapKeepsTwoFrames: 200000 bytes in 32 KiB reads leave frames 6
// and 7 in the replay, as the one Linux row of 683cc6a9 shows.
func TestLinkReplayCapKeepsTwoFrames(t *testing.T) {
	p := &managedProc{id: "r", bufCap: linkReplayCap}
	left := 200000
	for left > 0 {
		n := min(left, 32*1024)
		p.emit(streamFrame{Stream: "stdout", Data: strings.Repeat("A", (n+2)/3*4)})
		left -= n
	}
	if first, last := p.buffer[0].Seq, p.buffer[len(p.buffer)-1].Seq; first != 6 || last != 7 {
		t.Errorf("replay seq range = %d to %d, want 6 to 7", first, last)
	}
}

// TestPruneSpentIDs: the prune drops a spent id after spentAge only.
func TestPruneSpentIDs(t *testing.T) {
	m := newTestProcManager(t)
	now := time.Now()
	m.mu.Lock()
	m.spent["old"] = now.Add(-m.spentAge)
	m.spent["new"] = now.Add(-m.spentAge + time.Second)
	m.pruneSpentLocked(now)
	_, oldStays := m.spent["old"]
	_, newStays := m.spent["new"]
	m.mu.Unlock()
	if oldStays || !newStays {
		t.Errorf("after the prune old=%v new=%v, want old gone and new kept", oldStays, newStays)
	}
}

// TestLinkBoundConcurrentSpawnsStartOne: link-bound spawns of one id at the same
// time start one child only. The others get the spent-id refusal.
func TestLinkBoundConcurrentSpawnsStartOne(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	exe, env := helperCommand(t, "sleep")
	c, _ := pipeConn(t)
	const n = 8
	var started atomic.Int32
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			if _, err := m.spawnVia(c, "same", exe, []string{"60"}, "", env, false, nil, time.Minute); err == nil {
				started.Add(1)
			} else if err != errSpentID {
				t.Errorf("spawn error = %v, want the spent-id refusal", err)
			}
		}()
	}
	close(gate)
	wg.Wait()
	if got := started.Load(); got != 1 {
		t.Errorf("%d link-bound spawns of one id started, want 1", got)
	}
}

// TestLinkBoundNoEndAfterShutdownBegan: once the shutdown began (with
// -keep-children the drop of the connections starts each clock), a slow
// teardown sends a link-bound child no unattended signal.
func TestLinkBoundNoEndAfterShutdownBegan(t *testing.T) {
	var mu sync.Mutex
	var got []string
	m := newTestProcManager(t)
	exe, env := helperCommand(t, "sleep")
	c, _ := pipeConn(t)
	p, err := m.spawnVia(c, "kept", exe, []string{"60"}, "", env, false, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	real := signalGroup
	signalGroup = func(g *supervisor.ProcGroup, proc *os.Process, signame string) error {
		if proc.Pid == p.pid {
			mu.Lock()
			got = append(got, signame)
			mu.Unlock()
		}
		return real(g, proc, signame)
	}
	t.Cleanup(func() {
		signalGroup = real
		_ = real(p.group, p.cmd.Process, "KILL")
		<-p.done
	})
	// The shutdown begins, and then the teardown drops the connections.
	m.refuseStarts()
	m.detachConn(c)
	// The time is 1 s and the step 250 ms. Watch for a signal well past that.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		sent := append([]string(nil), got...)
		mu.Unlock()
		if len(sent) > 0 {
			t.Fatalf("a kept link-bound child got %v after the shutdown began", sent)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.unattendedFor(time.Now()) < time.Second {
		t.Fatal("the clock did not run, so the test proves nothing")
	}
}

// TestLinkBoundOtherConnCloseKeepsClock: the close of a connection that is not the
// link starts no clock.
func TestLinkBoundOtherConnCloseKeepsClock(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	exe, env := helperCommand(t, "sleep")
	link, _ := pipeConn(t)
	other, _ := pipeConn(t)
	p, err := m.spawnVia(link, "own", exe, []string{"60"}, "", env, false, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	m.detachConn(other)
	if d := p.unattendedFor(time.Now()); d != 0 {
		t.Errorf("unattendedFor = %s after a close of another connection, want 0", d)
	}
	m.detachConn(link)
	if p.unattendedFor(time.Now().Add(time.Second)) == 0 {
		t.Error("the close of the link started no clock")
	}
}

// TestPlainSpawnDoesNotReplaceLinkBound: a plain spawn that passed its id test
// before a link-bound spawn of that id put its record in the table ends its own
// child, gets the spent-id refusal and leaves the link-bound record in place.
func TestPlainSpawnDoesNotReplaceLinkBound(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	exe, env := helperCommand(t, "sleep")
	paused, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	oldBefore, oldAfter, oldSignal := beforeChildStart, afterChildStart, signalGroup
	var mu sync.Mutex
	var pids []int
	kills := map[int]int{}
	beforeChildStart = func(id string) {
		if id == "race" && first.CompareAndSwap(false, true) {
			close(paused)
			<-release
		}
	}
	afterChildStart = func(id string, pid int) {
		if id == "race" {
			mu.Lock()
			pids = append(pids, pid)
			mu.Unlock()
		}
	}
	signalGroup = func(g *supervisor.ProcGroup, proc *os.Process, signame string) error {
		mu.Lock()
		if signame == "KILL" {
			kills[proc.Pid]++
		}
		mu.Unlock()
		return oldSignal(g, proc, signame)
	}
	t.Cleanup(func() { beforeChildStart, afterChildStart, signalGroup = oldBefore, oldAfter, oldSignal })
	plainErr := make(chan error, 1)
	go func() {
		_, err := m.spawnVia(nil, "race", exe, []string{"60"}, "", env, false, nil, 0)
		plainErr <- err
	}()
	<-paused
	c, _ := pipeConn(t)
	linked, err := m.spawnVia(c, "race", exe, []string{"60"}, "", env, false, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-plainErr; err != errSpentID {
		t.Errorf("plain spawn = %v, want the spent-id refusal", err)
	}
	if got := m.get("race"); got != linked {
		t.Error("the plain spawn replaced the link-bound record")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pids) != 2 || pids[0] != linked.pid {
		t.Fatalf("started pids = %v, want the link-bound child and then the plain one", pids)
	}
	if kills[pids[1]] == 0 {
		t.Errorf("the child of the refused plain spawn got no KILL")
	}
}
