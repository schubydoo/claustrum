//go:build linux || darwin

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// holdRecordWrite holds the next record write right after its temp file exists. It
// returns a channel that closes when the write is held, and the release. The release
// also runs at the end of the test.
func holdRecordWrite(t *testing.T) (held <-chan struct{}, release func()) {
	t.Helper()
	h, r := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	release = func() { releaseOnce.Do(func() { close(r) }) }
	oldHook := recordTempMade
	recordTempMade = func() {
		once.Do(func() { close(h) })
		<-r
	}
	t.Cleanup(func() { recordTempMade = oldHook })
	t.Cleanup(release)
	return h, release
}

// spawnHeldSleeper spawns a 60 s sleeper in a goroutine and waits until its record
// write is held. The returned channel gives the result of the spawn.
func spawnHeldSleeper(t *testing.T, s *server, held <-chan struct{}) <-chan *managedProc {
	t.Helper()
	c, _ := pipeConn(t)
	exe, env := helperCommand(t, "sleep")
	spawned := make(chan *managedProc, 1)
	go func() {
		mp, err := s.procs.spawn(c, "rec", exe, []string{"60"}, "", env, true)
		if err != nil {
			t.Errorf("spawn: %v", err)
		}
		spawned <- mp
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the record write did not make its temp file")
	}
	return spawned
}

// awaitHeldSpawnEnd waits for the held spawn and then for the end of its child, so
// the exit line of the child does not land in the log capture of a later test.
func awaitHeldSpawnEnd(t *testing.T, spawned <-chan *managedProc) {
	t.Helper()
	var mp *managedProc
	select {
	case mp = <-spawned:
	case <-time.After(10 * time.Second):
		t.Fatal("the spawn did not return after the release")
	}
	if mp == nil {
		return
	}
	select {
	case <-mp.done:
	case <-time.After(10 * time.Second):
		t.Error("the child of the held spawn did not end in 10 s")
	}
}

// TestShutdownLeavesNoRecordTempFile holds the record write of a spawn right after its
// temp file exists, and runs the shutdown with the production trigger and teardown.
// The teardown does not end while the write is open. After the release the write
// ends, the kill of the teardown ends the child, and children/ is empty: no temp file
// and no record (claustrum's own fix for round task003-01, macOS VM, a 0-byte temp
// file left).
func TestShutdownLeavesNoRecordTempFile(t *testing.T) {
	// shutdownRecordWait keeps its production value. Without the fix the teardown
	// then ends at that bound while the write is open.
	s, sock, runDir := newTeardownServer(t)
	held, release := holdRecordWrite(t)
	spawned := spawnHeldSleeper(t, s, held)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.requestShutdown()
		s.closeAll(sock)
	}()
	children := filepath.Join(runDir, childrenDirName)
	select {
	case <-done:
		names := childrenNames(t, children)
		release()
		awaitHeldSpawnEnd(t, spawned)
		t.Fatalf("the shutdown ended while the record write was open, children/ held %v", names)
	case <-time.After(3*shutdownRecordWait + time.Second):
	}
	release()
	awaitHeldSpawnEnd(t, spawned)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the shutdown did not end after the release")
	}
	// The process exits here in production, so no temp file can stay.
	for _, n := range childrenNames(t, children) {
		if strings.HasPrefix(n, ".rec-") {
			t.Errorf("a temp file of the record stays after the shutdown: %s", n)
		}
	}
	// The kill ended the child, so its record goes too. On a slow runner the end
	// can come after the bound of the shutdown wait, so this polls.
	deadline := time.Now().Add(10 * time.Second)
	for len(childrenNames(t, children)) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if names := childrenNames(t, children); len(names) != 0 {
		t.Errorf("children/ after the shutdown: %v, want empty", names)
	}
}

// TestShutdownDropsConnectionsBeforeTheStartGate: a spawn that holds the start gate
// does not delay the close of the connections. Round task003-01 measured the close
// at the "shutdown requested" line on both builds (Linux VM, +0.0000 to +0.0001 s).
func TestShutdownDropsConnectionsBeforeTheStartGate(t *testing.T) {
	s, sock, _ := newTeardownServer(t)
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	// A reply proves that the server holds the connection.
	if _, err := nc.Write([]byte(authed(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`) + "\n")); err != nil {
		t.Fatal(err)
	}
	rd := bufio.NewReader(nc)
	if _, err := rd.ReadString('\n'); err != nil {
		t.Fatalf("ping reply: %v", err)
	}

	held, release := holdRecordWrite(t)
	spawned := spawnHeldSleeper(t, s, held)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.requestShutdown()
		s.closeAll(sock)
	}()

	_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = rd.ReadString('\n')
	var ne net.Error
	timedOut := errors.As(err, &ne) && ne.Timeout()
	select {
	case <-done:
		t.Error("the teardown ended while a spawn held the start gate")
	default:
	}
	release()
	awaitHeldSpawnEnd(t, spawned)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the teardown did not end after the release")
	}
	if timedOut || err == nil {
		t.Fatalf("the connection was not closed while a spawn held the start gate (read: %v)", err)
	}
}

// TestStartAfterShutdownBeganLeavesNoTrace holds a spawn right after the start of its
// child, begins the shutdown with the production trigger, and then lets the spawn go
// on. The child is dead when the spawn returns. The spawn logs no start line, has no
// table entry, writes no record and gets no frame.
func TestStartAfterShutdownBeganLeavesNoTrace(t *testing.T) {
	checkLateStartLeavesNoTrace(t, false)
}

// TestLateStartEndedWithKeepChildren is the same with -keep-children. The child of a
// late start is ended too, because its client got no reply and has no pid for it.
// The teardown then runs with the flag and keeps nothing of it.
func TestLateStartEndedWithKeepChildren(t *testing.T) {
	checkLateStartLeavesNoTrace(t, true)
}

func checkLateStartLeavesNoTrace(t *testing.T, keepChildren bool) {
	t.Helper()
	s, sock, runDir := newTeardownServer(t)
	s.keepChildren = keepChildren
	logs := captureLogBuf(t)
	held, release := make(chan int, 1), make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	t.Cleanup(letGo)
	oldHook := afterChildStart
	afterChildStart = func(id string, pid int) {
		if id == "late" {
			held <- pid
			<-release
		}
	}
	t.Cleanup(func() { afterChildStart = oldHook })

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
	var pid int
	select {
	case pid = <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the spawn did not start its child")
	}
	s.requestShutdown()
	letGo()
	var resp *response
	select {
	case resp = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("the held spawn did not return after the release")
	}
	s.closeAll(sock)
	if resp != nil {
		b, _ := json.Marshal(*resp)
		t.Errorf("the late start got the frame %s, want none", b)
	}
	// No signal here: the spawn reaped pid already, so the pid can belong to
	// another process now. A child in the table ends through endOwnChild below.
	if pidAlive(pid) {
		t.Errorf("the child %d of the late start is alive after the spawn returned", pid)
	}
	if strings.Contains(logs.String(), "Process late started") {
		t.Error("the log has a start line for the late start")
	}
	if p := s.procs.get("late"); p != nil {
		endOwnChild(t, p)
		t.Error("the late start is in the table")
	}
	if ents, err := os.ReadDir(filepath.Join(runDir, childrenDirName)); err == nil && len(ents) != 0 {
		t.Errorf("children/ after the late start holds %d entries, want none", len(ents))
	}
}
