package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTeardownServer boots a server on <tmp>/run/c1/rpc.sock with the accept wrap of
// production and no connection drop of its own, so closeAll is its one teardown. It
// returns the server, the socket and the run dir.
func newTeardownServer(t *testing.T) (*server, string, string) {
	t.Helper()
	base, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	runDir := filepath.Join(base, "run", "c1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(runDir, "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	s := newTestServer(t)
	s.ln = ln
	s.procs.runDir = execChildRunDir(sock)
	s.procs.holdRunDir()
	s.procs.instanceID = s.instanceID
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			ac := newActivityConn(nc)
			c := &conn{nc: ac, ac: ac}
			s.mu.Lock()
			s.conns[c] = struct{}{}
			s.mu.Unlock()
			go s.serveConn(c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		s.procs.killAll()
	})
	return s, sock, runDir
}

// TestNoChildStartsAfterShutdownBegan holds a process.spawn just before the start of
// its child and runs the shutdown with the production trigger and teardown. Then it
// lets the spawn go on. The spawn starts no child, writes no record and gets no
// frame.
func TestNoChildStartsAfterShutdownBegan(t *testing.T) {
	s, sock, runDir := newTeardownServer(t)
	logs := captureLogBuf(t)

	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	oldHook := beforeChildStart
	beforeChildStart = func(string) {
		once.Do(func() { close(held) })
		<-release
	}
	t.Cleanup(func() { beforeChildStart = oldHook })
	// The start itself must not run, so the seam after it counts the starts.
	var started atomic.Int32
	oldAfter := afterChildStart
	afterChildStart = func(string, int) { started.Add(1) }
	t.Cleanup(func() { afterChildStart = oldAfter })

	c, _ := pipeConn(t)
	exe, env := helperCommand(t, "sleep")
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": "late", "command": exe, "args": []string{"60"}, "env": env},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan *response, 1)
	go func() { got <- s.dispatch(c, body) }()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("the spawn did not reach the start of its child")
	}
	s.requestShutdown()
	s.closeAll(sock)
	close(release)

	var resp *response
	select {
	case resp = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("the held spawn did not return after the release")
	}
	if resp != nil {
		b, _ := json.Marshal(*resp)
		t.Errorf("the late spawn got the frame %s, want none", b)
	}
	if n := started.Load(); n != 0 {
		t.Errorf("the late spawn ran the start of a child %d time(s), want none", n)
	}
	if ents, err := os.ReadDir(filepath.Join(runDir, childrenDirName)); err == nil && len(ents) != 0 {
		t.Errorf("the late spawn left %d entries in children/, want none", len(ents))
	}
	if strings.Contains(logs.String(), "Process late started") {
		t.Error("the log has a start line for the late spawn")
	}
	if p := s.procs.get("late"); p != nil {
		endOwnChild(t, p)
		t.Errorf("a child (pid %d) started after the shutdown began", p.pid)
	}
}

// TestLateSessionSpawnSupersedesNothing: a session spawn that comes after the close of
// the start gate sends no signal to the prior process of its session. That rule is
// claustrum's own and not measured.
func TestLateSessionSpawnSupersedesNothing(t *testing.T) {
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	cat, env := helperCommand(t, "cat")
	first, err := m.spawn(c, "s1", cat, sessionArgs, "", env, false)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() { endOwnChild(t, first) })
	signals := countSignalsTo(t, first)
	m.stopStarts()
	if _, err := m.spawn(c, "s2", cat, sessionArgs, "", env, false); !errors.Is(err, errStartsStopped) {
		t.Fatalf("the spawn after the close of the gate answered %v, want errStartsStopped", err)
	}
	// The supersede runs inside the spawn, so the count is final here.
	if n := signals.Load(); n != 0 {
		t.Errorf("the late session spawn sent %d signal(s) to the prior process, want none", n)
	}
	if m.get("s2") != nil {
		t.Error("the late session spawn is in the table")
	}
}

// TestHeldSpawnsLeaveNoTraceAtShutdown holds 3 spawns past their check before the
// start and 10 spawns before that check. The production trigger then begins the
// shutdown. It does not block. The 10 spawns give up at once and start no child. The
// teardown waits for the 3. Their starts return after the shutdown began, so they
// get no start line, no table entry and no frame.
func TestHeldSpawnsLeaveNoTraceAtShutdown(t *testing.T) {
	s, sock, runDir := newTeardownServer(t)
	logs := captureLogBuf(t)
	const past, before = 3, 10
	reachedPast, reachedBefore := make(chan string, past), make(chan string, before)
	releasePast, releaseBefore := make(chan struct{}), make(chan struct{})
	var relPast, relBefore sync.Once
	letPast := func() { relPast.Do(func() { close(releasePast) }) }
	letBefore := func() { relBefore.Do(func() { close(releaseBefore) }) }
	t.Cleanup(letPast)
	t.Cleanup(letBefore)
	oldBefore, oldPast := beforeChildStart, pastStartCheck
	beforeChildStart = func(id string) {
		if strings.HasPrefix(id, "b") {
			reachedBefore <- id
			<-releaseBefore
		}
	}
	pastStartCheck = func(id string) {
		if strings.HasPrefix(id, "p") {
			reachedPast <- id
			<-releasePast
		}
	}
	t.Cleanup(func() { beforeChildStart, pastStartCheck = oldBefore, oldPast })

	c, _ := pipeConn(t)
	exe, env := helperCommand(t, "sleep")
	spawnReq := func(id string) []byte {
		b, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken,
			"params": map[string]any{"id": id, "command": exe, "args": []string{"60"}, "env": env},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	type result struct {
		id   string
		resp *response
	}
	results := make(chan result, past+before)
	wait := func(ch chan string, n int) {
		for range n {
			select {
			case <-ch:
			case <-time.After(10 * time.Second):
				t.Fatal("a spawn did not reach its hold")
			}
		}
	}
	for i := range past {
		id := "p" + strconv.Itoa(i)
		go func() { results <- result{id, s.dispatch(c, spawnReq(id))} }()
	}
	wait(reachedPast, past)
	for i := range before {
		id := "b" + strconv.Itoa(i)
		go func() { results <- result{id, s.dispatch(c, spawnReq(id))} }()
	}
	wait(reachedBefore, before)

	begun := make(chan struct{})
	go func() { defer close(begun); s.requestShutdown() }()
	select {
	case <-begun:
	case <-time.After(5 * time.Second):
		t.Fatal("the start of the shutdown blocked on the held spawns")
	}
	letBefore()
	got := map[string]*response{}
	for range before {
		select {
		case r := <-results:
			got[r.id] = r.resp
		case <-time.After(10 * time.Second):
			t.Fatal("a spawn before the check did not return")
		}
	}
	done := make(chan struct{})
	go func() { defer close(done); s.closeAll(sock) }()
	select {
	case <-done:
		t.Error("the teardown ended while 3 spawns held the start gate")
	case <-time.After(200 * time.Millisecond):
	}
	letPast()
	for range past {
		select {
		case r := <-results:
			got[r.id] = r.resp
		case <-time.After(10 * time.Second):
			t.Fatal("a spawn past the check did not return")
		}
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the teardown did not end")
	}

	for id, resp := range got {
		if resp != nil {
			b, _ := json.Marshal(*resp)
			t.Errorf("spawn %s got the frame %s, want none", id, b)
		}
		if p := s.procs.get(id); p != nil {
			endOwnChild(t, p)
			t.Errorf("spawn %s is in the table", id)
		}
		if strings.Contains(logs.String(), "Process "+id+" started") {
			t.Errorf("the log has a start line for spawn %s", id)
		}
	}
	if len(got) != past+before {
		t.Errorf("%d spawns returned, want %d", len(got), past+before)
	}
	if !strings.Contains(logs.String(), "killed 0 child process group(s)") {
		t.Errorf("the cleanup line counts a child:\n%s", logs.String())
	}
	children := filepath.Join(runDir, childrenDirName)
	deadline := time.Now().Add(10 * time.Second)
	for {
		ents, err := os.ReadDir(children)
		if err != nil || len(ents) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("children/ holds %d entries after the shutdown, want none", len(ents))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}
