package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The daemon's own lines that VMs measured against f6010b97 and 89cb6289 (slice E of the
// 89cb6289 reconciliation): the listening line and the lines of a clean shutdown. These
// tests run on linux, macOS and Windows. Every child here is a helper of this test binary
// that the daemon under test spawned, and the daemon's own shutdown ends it.

// bootLineServer boots a daemon in-process on a short temp socket, with its accept loops
// running. It runs no cleaner and no os.Exit. The caller runs closeAll.
func bootLineServer(t *testing.T, keepChildren bool) (*server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	s, err := newServerOnSocket(sock, testToken, "", wireLogOptions{}, keepChildren, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.procs.killAll() }) // a failed run leaves no helper behind
	t.Cleanup(func() { _ = s.ln.Close() })
	s.startAcceptLoops()
	return s, sock
}

// spawnLineSleeper spawns one helper sleeper over the socket on its own connection. The
// connection stays open, and spawnLineSleeper returns it.
func spawnLineSleeper(t *testing.T, sock, id string) *testClient {
	t.Helper()
	exe, env := helperCommand(t, "sleep")
	params, err := json.Marshal(map[string]any{"id": id, "command": exe, "args": []string{"60"}, "env": env})
	if err != nil {
		t.Fatal(err)
	}
	cl := dial(t, sock)
	reply := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.spawn","params":` + string(params) + `}`)))
	if !strings.Contains(reply, `"success":true`) {
		t.Fatalf("spawn %s = %s", id, reply)
	}
	return cl
}

// wantLinesInOrder asserts that got holds each line of want, in that order.
func wantLinesInOrder(t *testing.T, got string, want ...string) {
	t.Helper()
	at := 0
	for _, w := range want {
		i := strings.Index(got[at:], w+"\n")
		if i < 0 {
			t.Errorf("log lacks %q after offset %d\nlog:\n%s", w, at, got)
			return
		}
		at += i + len(w)
	}
}

// TestListeningLine: the listening line ends with the pid and the instance. The instance
// is the instanceId of server.capabilities (Linux VM, 36 of 36 daemons of 89cb6289).
// The name stays "Claustrum".
func TestListeningLine(t *testing.T) {
	s, sock := bootLineServer(t, false)
	t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })

	line := s.listeningLine(sock)
	m := regexp.MustCompile(`^Claustrum remote server listening on (.+) \(pid (\d+), instance ([0-9a-f]{32})\)\n$`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("listening line = %q, want `Claustrum remote server listening on <S> (pid <n>, instance <32 hex>)`", line)
	}
	if m[1] != sock || m[2] != fmt.Sprint(os.Getpid()) {
		t.Errorf("listening line names socket %q and pid %s, want %q and %d", m[1], m[2], sock, os.Getpid())
	}
	var reply struct {
		Result struct {
			InstanceID string `json:"instanceId"`
		} `json:"result"`
	}
	raw := dial(t, sock).call(authed(`{"jsonrpc":"2.0","id":1,"method":"server.capabilities"}`))
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("capabilities reply %s: %v", raw, err)
	}
	if reply.Result.InstanceID == "" || m[3] != reply.Result.InstanceID {
		t.Errorf("listening line instance %q, server.capabilities instanceId %q, want one value", m[3], reply.Result.InstanceID)
	}
}

// TestShutdownBySignalLines: SIGTERM with two connections and two children gives the three
// lines, with the counts of both (rows RP11, RP12b, ST03).
func TestShutdownBySignalLines(t *testing.T) {
	s, sock := bootLineServer(t, false)
	spawnLineSleeper(t, sock, "c1")
	spawnLineSleeper(t, sock, "c2")
	buf := captureLogBuf(t)

	s.shutdownOnSignal(syscall.SIGTERM)
	s.closeAll(sock)

	wantLinesInOrder(t, buf.String(),
		"[daemon] received terminated; shutting down (children will be killed)",
		"[Server] shutdown requested",
		"[Server] cleanup: closed 2 connection(s), killed 2 child process group(s)")
	// The kill is real: each child ends.
	for _, id := range []string{"c1", "c2"} {
		select {
		case <-s.procs.get(id).done:
		case <-time.After(10 * time.Second):
			t.Errorf("child %s did not end after the shutdown kill", id)
		}
	}
}

// TestShutdownCountLeavesOutAnEndedChild: one child ended by itself before the shutdown,
// and one still runs. The cleanup line counts the kill of the running child only.
func TestShutdownCountLeavesOutAnEndedChild(t *testing.T) {
	s, sock := bootLineServer(t, false)
	spawnLineSleeper(t, sock, "c1")
	exe, env := helperCommand(t, "echo")
	params, err := json.Marshal(map[string]any{"id": "e1", "command": exe, "args": []string{"hi"}, "env": env})
	if err != nil {
		t.Fatal(err)
	}
	cl := dial(t, sock)
	if reply := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.spawn","params":` + string(params) + `}`))); !strings.Contains(reply, `"success":true`) {
		t.Fatalf("spawn e1 = %s", reply)
	}
	select {
	case <-s.procs.get("e1").done:
	case <-time.After(10 * time.Second):
		t.Fatal("the echo helper did not end within 10 s")
	}
	buf := captureLogBuf(t)
	s.shutdownOnSignal(syscall.SIGTERM)
	s.closeAll(sock)
	wantLinesInOrder(t, buf.String(),
		"[Server] cleanup: closed 2 connection(s), killed 1 child process group(s)")
	select {
	case <-s.procs.get("c1").done:
	case <-time.After(10 * time.Second):
		t.Error("child c1 did not end after the shutdown kill")
	}
}

// TestShutdownByRPCLines: server.shutdown, as -stop sends it, gives the cleanup line. The
// count of connections includes the connection of the request (rows RP12a, WJ03, WN03).
func TestShutdownByRPCLines(t *testing.T) {
	s, sock := bootLineServer(t, false)
	spawnLineSleeper(t, sock, "c1")
	spawnLineSleeper(t, sock, "c2")
	buf := captureLogBuf(t)

	stop := dial(t, sock)
	stop.send(`{"jsonrpc":"2.0","id":2,"method":"server.shutdown","params":{}}`) // unauthenticated, as -stop sends it
	select {
	case <-s.shutdown:
	case <-time.After(5 * time.Second):
		t.Fatal("server.shutdown did not request the shutdown")
	}
	s.closeAll(sock)

	wantLinesInOrder(t, buf.String(),
		"[ServerHandler] server.shutdown received over RPC",
		"[Server] shutdown requested",
		"[Server] cleanup: closed 3 connection(s), killed 2 child process group(s)")
	// A clean stop ends the children (row WJ03 on Windows, row RP12a on Linux).
	for _, id := range []string{"c1", "c2"} {
		select {
		case <-s.procs.get(id).done:
		case <-time.After(10 * time.Second):
			t.Errorf("child %s did not end after the clean stop", id)
		}
	}
}

// TestShutdownLinesWithNoClient: no connection and no child give zeros (row ST03).
func TestShutdownLinesWithNoClient(t *testing.T) {
	s, sock := bootLineServer(t, false)
	buf := captureLogBuf(t)
	s.shutdownOnSignal(syscall.SIGTERM)
	s.closeAll(sock)
	wantLinesInOrder(t, buf.String(),
		"[daemon] received terminated; shutting down (children will be killed)",
		"[Server] shutdown requested",
		"[Server] cleanup: closed 0 connection(s), killed 0 child process group(s)")
}

// TestShutdownLinesKeepChildren: with -keep-children the first line says "kept" and the
// cleanup line counts no killed child. The flag is claustrum's own. No row measures it.
func TestShutdownLinesKeepChildren(t *testing.T) {
	s, sock := bootLineServer(t, true)
	spawnLineSleeper(t, sock, "c1")
	buf := captureLogBuf(t)
	s.shutdownOnSignal(syscall.SIGTERM)
	s.closeAll(sock)
	wantLinesInOrder(t, buf.String(),
		"[daemon] received terminated; shutting down (children will be kept)",
		"[Server] shutdown requested",
		"[Server] -keep-children: leaving 1 running child process(es) alive across shutdown",
		"[Server] cleanup: closed 1 connection(s), killed 0 child process group(s)")
	p := s.procs.get("c1")
	if !p.isRunning() {
		t.Error("the child ended at a -keep-children shutdown")
	}
	// End the kept child here and wait for its exit. Its exit line then does not land in
	// the log buffer of a later test.
	s.procs.kill("c1", "KILL")
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Error("the kept child did not end within 10 s of the kill")
	}
}

// TestExitLineOfAKilledChild: the exit line of a child that process.kill ended carries
// ", signalled at client request". Measured on a Windows VM against 89cb6289 in all 10 kill
// rows ("Process c1 exited with code 1, signalled at client request"), and on Linux (rows
// SP03a, SP03b). A child that ends by itself gets no such tail.
func TestExitLineOfAKilledChild(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	c, _ := pipeConn(t)
	exe, env := helperCommand(t, "sleep")
	killed, err := m.spawn(c, "k1", exe, []string{"60"}, "", env, true)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	buf := captureLogBuf(t)
	m.kill("k1", "KILL")
	echo, envEcho := helperCommand(t, "echo")
	plain, err := m.spawn(c, "p1", echo, []string{"hi"}, "", envEcho, true)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	for _, p := range []*managedProc{killed, plain} {
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("process %s did not end", p.id)
		}
	}
	log := buf.String()
	if !regexp.MustCompile(`\[process\.Manager\] Process k1 exited with code -?\d+(, terminated by SIGKILL)?, signalled at client request\n`).MatchString(log) {
		t.Errorf("log lacks the exit line of the killed child with its tail:\n%s", log)
	}
	if !strings.Contains(log, "[process.Manager] Process p1 exited with code 0\n") {
		t.Errorf("log lacks the plain exit line of the child that ended by itself:\n%s", log)
	}
}

// TestDrainGraceLine: a grandchild holds the stdout of a child that has ended. When the
// drain grace is over, the daemon logs the reference's line and then closes the pipes.
// Measured on Linux (row SP05) and on a Windows VM against 89cb6289 (the rows with kids
// that inherit the stdout and stderr of the child).
func TestDrainGraceLine(t *testing.T) {
	oldDrain := exitDrainGrace
	exitDrainGrace = 200 * time.Millisecond
	t.Cleanup(func() { exitDrainGrace = oldDrain })
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	c, _ := pipeConn(t)
	exe, env := helperCommand(t, "orphan-stdout")
	buf := captureLogBuf(t)
	p, err := m.spawn(c, "d1", exe, []string{"3"}, "", env, true) // the grandchild holds stdout for 3 s
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("no exit of the managed process")
	}
	wantLinesInOrder(t, buf.String(),
		"[process.Manager] Process d1: pipe drain grace expired (grandchild holding stdio?); force-closing",
		"[process.Manager] Process d1 exited with code 7")
}
