package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// linkSpawnReq builds a process.spawn request of a helper mode with extra params.
func linkSpawnReq(t *testing.T, id int, procID, mode string, args []string, extra map[string]any) string {
	t.Helper()
	exe, env := helperCommand(t, mode)
	params := map[string]any{"id": procID, "command": exe, "args": args, "env": env}
	for k, v := range extra {
		params[k] = v
	}
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	return string(b)
}

// framesOf returns the frames of processID that cl got so far.
func (cl *testClient) framesOf(processID string) []streamFrame {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	var out []streamFrame
	for _, f := range cl.fr {
		if f.ProcessID == processID {
			out = append(out, f)
		}
	}
	return out
}

// TestSocketLinkBoundRefusals pins the two refusals of 683cc6a9 (measured on
// Linux): linkBound with a launcher gets its own text and reason other, and with
// "pty":true plain Invalid params with no data.
func TestSocketLinkBoundRefusals(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	launcher := string(cl.call(linkSpawnReq(t, 1, "lb1", "sleep", []string{"0"}, map[string]any{
		"linkBound": map[string]any{}, "launcher": []string{"/usr/bin/env"},
	})))
	if want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"linkBound cannot be combined with pty or launcher","data":{"reason":"other"}}}`; launcher != want {
		t.Errorf("launcher reply = %s\nwant %s", launcher, want)
	}
	pty := string(cl.call(linkSpawnReq(t, 2, "lb2", "sleep", []string{"0"}, map[string]any{
		"linkBound": map[string]any{}, "pty": true,
	})))
	if want := `{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"Invalid params"}}`; pty != want {
		t.Errorf("pty reply = %s\nwant %s", pty, want)
	}
	ok := string(cl.call(linkSpawnReq(t, 3, "lb3", "sleep", []string{"0"}, map[string]any{
		"linkBound": map[string]any{"unattendedSeconds": 4},
	})))
	if want := `{"jsonrpc":"2.0","id":3,"result":{"success":true}}`; ok != want {
		t.Errorf("spawn reply = %s\nwant %s", ok, want)
	}
	assertGolden(t, "socket_process_linkbound_refusals.golden.json",
		encodeGolden(t, []json.RawMessage{json.RawMessage(launcher), json.RawMessage(pty), json.RawMessage(ok)}))
}

// TestSocketLinkBoundStaysOnLink pins the link of 683cc6a9 (measured on Linux):
// stdin from another connection answers Process not found, the link writes it,
// and a reattach from another connection gets found true with the seq range of
// the link. That B gets the replay, no live frame, and that A stays open is
// claustrum's own, not measured.
func TestSocketLinkBoundStaysOnLink(t *testing.T) {
	sock := startSocketServer(t)
	a, b := dial(t, sock), dial(t, sock)
	if got := string(a.call(linkSpawnReq(t, 1, "lc", "cat", []string{}, map[string]any{
		"linkBound": map[string]any{"unattendedSeconds": 120},
	}))); !strings.Contains(got, `"success":true`) {
		t.Fatalf("spawn = %s", got)
	}
	notFound := string(b.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.stdin","params":{"id":"lc","data":"aGkK"}}`)))
	if want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Process not found"}}`; notFound != want {
		t.Errorf("stdin from B = %s\nwant %s", notFound, want)
	}
	applied := string(a.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.stdin","params":{"id":"lc","data":"aGkK"}}`)))
	if want := `{"jsonrpc":"2.0","id":2,"result":{"success":true,"applied":3}}`; applied != want {
		t.Errorf("stdin from A = %s\nwant %s", applied, want)
	}
	a.wait(func() bool { return len(a.fr) >= 1 })
	if f := a.framesOf("lc"); len(f) != 1 || f[0].Data != "aGkK" || f[0].Seq != 1 {
		t.Fatalf("frames on A = %+v, want one frame of aGkK at seq 1", f)
	}
	reB := string(b.call(authed(`{"jsonrpc":"2.0","id":3,"method":"process.reattach","params":{"id":"lc","fromSeq":0}}`)))
	if want := `{"jsonrpc":"2.0","id":3,"result":{"found":true,"running":true,"firstSeq":1,"lastSeq":1,"stdinApplied":3}}`; reB != want {
		t.Errorf("reattach from B = %s\nwant %s", reB, want)
	}
	// A is still the link: it writes stdin and gets the next frame, B does not.
	a.call(authed(`{"jsonrpc":"2.0","id":4,"method":"process.stdin","params":{"id":"lc","data":"aGkK"}}`))
	a.wait(func() bool { return len(a.fr) >= 2 })
	reA := string(a.call(authed(`{"jsonrpc":"2.0","id":5,"method":"process.reattach","params":{"id":"lc","fromSeq":2}}`)))
	if want := `{"jsonrpc":"2.0","id":5,"result":{"found":true,"running":true,"firstSeq":1,"lastSeq":2,"stdinApplied":6}}`; reA != want {
		t.Errorf("reattach from A = %s\nwant %s", reA, want)
	}
	for _, f := range b.framesOf("lc") {
		if f.Seq > 1 {
			t.Errorf("B got live frame %+v, want the stream on the link only", f)
		}
	}
	// B ends the process. The exit frame goes to the link, and the record is gone
	// at once, so B's next reattach answers found false.
	b.call(authed(`{"jsonrpc":"2.0","id":6,"method":"process.kill","params":{"id":"lc","signal":"KILL"}}`))
	a.waitExit("lc")
	gone := string(b.call(authed(`{"jsonrpc":"2.0","id":7,"method":"process.reattach","params":{"id":"lc","fromSeq":0}}`)))
	if want := `{"jsonrpc":"2.0","id":7,"result":{"found":false,"running":false,"firstSeq":0,"lastSeq":0,"stdinApplied":0}}`; gone != want {
		t.Errorf("reattach after the exit = %s\nwant %s", gone, want)
	}
}

// TestSocketLinkBoundSpentID pins the exit frame and the spent id of 683cc6a9
// (measured on Linux): the exit frame has no killedBy and no signal, a plain spawn
// of the id is refused and its child does not run, and process.kill answers
// success.
func TestSocketLinkBoundSpentID(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.call(linkSpawnReq(t, 1, "x", "sleep", []string{"0"}, map[string]any{"linkBound": map[string]any{}}))
	frames := cl.waitExit("x")
	exit, err := json.Marshal(frames[len(frames)-1])
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"type":"stream","processId":"x","stream":"exit","seq":1,"exitCode":0}`; string(exit) != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
	mark := filepath.Join(t.TempDir(), "ran")
	refused := string(cl.call(linkSpawnReq(t, 2, "x", "filter-mark", []string{mark}, nil)))
	if want := `{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"this id is in use, or was a terminal's or a link-bound process's: take a fresh one","data":{"reason":"other"}}}`; refused != want {
		t.Errorf("spawn of a spent id = %s\nwant %s", refused, want)
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Errorf("the child of a refused spawn ran: stat = %v", err)
	}
	kill := string(cl.call(authed(`{"jsonrpc":"2.0","id":3,"method":"process.kill","params":{"id":"x"}}`)))
	if want := `{"jsonrpc":"2.0","id":3,"result":{"success":true}}`; kill != want {
		t.Errorf("kill of a spent id = %s\nwant %s", kill, want)
	}
	assertGolden(t, "socket_process_linkbound_spent.golden.json",
		encodeGolden(t, []json.RawMessage{exit, json.RawMessage(refused), json.RawMessage(kill)}))
}

// TestSocketLinkBoundSpentIDExpires: once the spent age has passed, a spawn of the
// id starts again. 683cc6a9 starts it 931.8 s after the exit (measured on Linux).
func TestSocketLinkBoundSpentIDExpires(t *testing.T) {
	old := linkSpentAge
	linkSpentAge = 50 * time.Millisecond
	t.Cleanup(func() { linkSpentAge = old })
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.call(linkSpawnReq(t, 1, "y", "sleep", []string{"0"}, map[string]any{"linkBound": map[string]any{}}))
	cl.waitExit("y")
	deadline := time.Now().Add(5 * time.Second)
	for id := 2; ; id++ {
		got := string(cl.call(linkSpawnReq(t, id, "y", "sleep", []string{"0"}, nil)))
		if strings.Contains(got, `"success":true`) {
			return
		}
		if !strings.Contains(got, "take a fresh one") || time.Now().After(deadline) {
			t.Fatalf("spawn after the spent age = %s", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLinkBoundGoneAtSpawn: a link whose connection is already gone makes the
// process detached at once, so it ends after its unattended time with no
// detachConn. The end line is the one of 683cc6a9 (measured on Linux).
func TestLinkBoundGoneAtSpawn(t *testing.T) {
	buf := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	exe, env := helperCommand(t, "sleep")
	p, err := m.spawnVia(&conn{closed: true}, "gone1", exe, []string{"60"}, "", env, false, nil, time.Second)
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

// TestUnattendedTTLAndTick pins the clamp of 683cc6a9 (measured on Linux for each
// value here but -3) and the tick, which is claustrum's own.
func TestUnattendedTTLAndTick(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
		tick time.Duration
	}{
		{-3, "1m0s", 15 * time.Second},
		{0, "1m0s", 15 * time.Second},
		{1, "1s", 250 * time.Millisecond},
		{2, "2s", 500 * time.Millisecond},
		{4, "4s", time.Second},
		{120, "2m0s", 30 * time.Second},
		{599, "9m59s", time.Minute},
		{600, "10m0s", time.Minute},
		{100000, "10m0s", time.Minute},
	} {
		ttl := unattendedTTL(c.n)
		if ttl.String() != c.want {
			t.Errorf("unattendedTTL(%d) = %s, want %s", c.n, ttl, c.want)
		}
		if got := unattendedTick(ttl); got != c.tick {
			t.Errorf("unattendedTick(%s) = %s, want %s", ttl, got, c.tick)
		}
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
	signalGroup = func(g *procGroup, proc *os.Process, signame string) error {
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

// TestUnattendedEndGraceIsSeven pins the wait between the SIGHUP and the SIGKILL.
// 683cc6a9 logs the SIGKILL exit about 7 s after the ending line (Linux).
func TestUnattendedEndGraceIsSeven(t *testing.T) {
	if unattendedEndGrace != 7*time.Second {
		t.Errorf("unattendedEndGrace = %s, want 7s", unattendedEndGrace)
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
	signalGroup = func(g *procGroup, proc *os.Process, signame string) error {
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
