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

	"github.com/schubydoo/claustrum/internal/shellenv"
	"github.com/schubydoo/claustrum/internal/wirelog"
)

// The daemon's own lines that were measured against f6010b97 and
// 89cb6289: the listening line and the lines of a clean shutdown. These
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
	s, err := newServerOnSocket(sock, testToken, "", wirelog.Options{}, keepChildren, false)
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

// signalServingDaemon gives sig to s the way a daemon that serves gets it: through
// watchSignals, after the attach of the server. It returns when s requested the stop.
func signalServingDaemon(t *testing.T, s *server, sig os.Signal) {
	t.Helper()
	sigc := make(chan os.Signal, 1)
	watchSignals(sigc, s.keepChildren).attach(s)
	sigc <- sig
	select {
	case <-s.shutdown:
	case <-time.After(10 * time.Second):
		t.Fatal("the signal did not request the shutdown within 10 s")
	}
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
// is the instanceId of server.capabilities (Linux, 36 of 36 daemons of 89cb6289). The
// name stays "Claustrum".
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
// lines, with the counts of both (measured).
func TestShutdownBySignalLines(t *testing.T) {
	s, sock := bootLineServer(t, false)
	spawnLineSleeper(t, sock, "c1")
	spawnLineSleeper(t, sock, "c2")
	buf := captureLogBuf(t)

	signalServingDaemon(t, s, syscall.SIGTERM)
	s.closeAll(sock)

	wantLinesInOrder(t, buf.String(),
		"[daemon] received terminated; shutting down (children will be killed)",
		"[Server] shutdown requested",
		"[Server] cleanup: closed 2 connection(s), ended 2 child process group(s)")
	// The kill is real: each child ends.
	for _, id := range []string{"c1", "c2"} {
		select {
		case <-s.procs.get(id).done:
		case <-time.After(10 * time.Second):
			t.Errorf("child %s did not end after the shutdown kill", id)
		}
	}
}

// TestSignalWatchHandlesOneSignal pins a rule of 89cb6289 (measured on Linux): of two SIGTERM in the
// start, the daemon handles one. It logs one "received" line, and the attach of the server then logs
// one "shutdown requested" line. The second signal stays unread.
func TestSignalWatchHandlesOneSignal(t *testing.T) {
	logs := captureLogBuf(t)
	sigc := make(chan os.Signal, 2)
	sigc <- syscall.SIGTERM
	sigc <- syscall.SIGTERM
	w := watchSignals(sigc, false)
	fired := func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.fired
	}
	deadline := time.Now().Add(10 * time.Second)
	for !fired() {
		if time.Now().After(deadline) {
			t.Fatal("the watch handled no signal within 10 s")
		}
		time.Sleep(time.Millisecond)
	}
	s := &server{shutdown: make(chan struct{})}
	w.attach(s)

	select {
	case <-s.shutdown:
	default:
		t.Error("the attach after a signal did not request the shutdown")
	}
	if n := len(sigc); n != 1 {
		t.Errorf("%d signal(s) left unread, want 1", n)
	}
	out := logs.String()
	if n := strings.Count(out, "[daemon] received terminated; shutting down (children will be killed)\n"); n != 1 {
		t.Errorf("%d received line(s), want 1:\n%s", n, out)
	}
	if n := strings.Count(out, "[Server] shutdown requested\n"); n != 1 {
		t.Errorf("%d shutdown line(s), want 1:\n%s", n, out)
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
	signalServingDaemon(t, s, syscall.SIGTERM)
	s.closeAll(sock)
	wantLinesInOrder(t, buf.String(),
		"[Server] cleanup: closed 2 connection(s), ended 1 child process group(s)")
	select {
	case <-s.procs.get("c1").done:
	case <-time.After(10 * time.Second):
		t.Error("child c1 did not end after the shutdown kill")
	}
}

// TestShutdownByRPCLines: server.shutdown, as -stop sends it, gives the cleanup line. The
// count of connections includes the connection of the request (measured).
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
		"[Server] cleanup: closed 3 connection(s), ended 2 child process group(s)")
	// A clean stop ends the children (measured on Linux and Windows).
	for _, id := range []string{"c1", "c2"} {
		select {
		case <-s.procs.get(id).done:
		case <-time.After(10 * time.Second):
			t.Errorf("child %s did not end after the clean stop", id)
		}
	}
}

// waitConnsExactly polls until s tracks n connections, with a 5 s limit.
func waitConnsExactly(t *testing.T, s *server, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := s.connCount()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon tracks %d connection(s), want %d", got, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestShutdownCountKeepsAClosedStopConnection: one idle client, and a client that sends
// server.shutdown and closes after the reply, before the cleanup. The cleanup line still
// counts both connections, as 5fd08069 does (measured on Windows).
func TestShutdownCountKeepsAClosedStopConnection(t *testing.T) {
	s, sock := bootLineServer(t, false)
	dial(t, sock) // the idle client
	waitConnsExactly(t, s, 1)
	buf := captureLogBuf(t)

	stop := dial(t, sock)
	reply := string(stop.call(`{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}`))
	if reply != `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` {
		t.Fatalf("server.shutdown reply = %s", reply)
	}
	_ = stop.nc.Close()
	waitConnsExactly(t, s, 1) // the stop connection left before the cleanup
	s.closeAll(sock)

	wantLinesInOrder(t, buf.String(),
		"[Server] cleanup: closed 2 connection(s), ended 0 child process group(s)")
}

// TestShutdownLinesWithNoClient: no connection and no child give zeros (measured).
func TestShutdownLinesWithNoClient(t *testing.T) {
	s, sock := bootLineServer(t, false)
	buf := captureLogBuf(t)
	signalServingDaemon(t, s, syscall.SIGTERM)
	s.closeAll(sock)
	wantLinesInOrder(t, buf.String(),
		"[daemon] received terminated; shutting down (children will be killed)",
		"[Server] shutdown requested",
		"[Server] cleanup: closed 0 connection(s), ended 0 child process group(s)")
}

// TestShutdownLinesKeepChildren: with -keep-children the first line says "kept" and the
// cleanup line counts no killed child. The flag is claustrum's own. No row measures it.
func TestShutdownLinesKeepChildren(t *testing.T) {
	s, sock := bootLineServer(t, true)
	spawnLineSleeper(t, sock, "c1")
	buf := captureLogBuf(t)
	signalServingDaemon(t, s, syscall.SIGTERM)
	s.closeAll(sock)
	wantLinesInOrder(t, buf.String(),
		"[daemon] received terminated; shutting down (children will be kept)",
		"[Server] shutdown requested",
		"[Server] -keep-children: leaving 1 running child process(es) alive across shutdown",
		"[Server] cleanup: closed 1 connection(s), ended 0 child process group(s)")
	p := s.procs.get("c1")
	if !p.isRunning() {
		t.Error("the child ended at a -keep-children shutdown")
	}
	// End the kept child here and wait for its exit. Its exit line then does not land in
	// the log buffer of a later test.
	_ = s.procs.kill("c1", "KILL")
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Error("the kept child did not end within 10 s of the kill")
	}
}

// TestShutdownEndsTheLoginPATHRead: the cleanup ends a read of the login-shell PATH
// that still runs (52d82f0a, Linux and macOS). No failure line follows, and the
// cleanup line does not count its shell (52d82f0a, Linux).
func TestShutdownEndsTheLoginPATHRead(t *testing.T) {
	s, sock := bootLineServer(t, false)
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	shellenv.WarmAtStart()
	r := startedRead(t, f)
	s.signalShutdown()
	s.closeAll(sock)
	select {
	case <-r.end:
	default:
		t.Fatal("the cleanup left the read of the login-shell PATH running")
	}
	if got := shellenvLines(buf); got != "" {
		t.Errorf("the stopped read logged:\n%s", got)
	}
	wantLinesInOrder(t, buf.String(), "[Server] cleanup: closed 0 connection(s), ended 0 child process group(s)")
}
