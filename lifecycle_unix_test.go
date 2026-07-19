//go:build unix

package main

import (
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests drive the real daemon-lifecycle shells — runServe,
// daemonizeWithToken, run, teardown — in-process via the osExit stub
// (exitstub_test.go), where the socket harness (newRunningServer) deliberately
// bypasses them. Unix-only: they lean on syscall.Open fds, self-delivered
// SIGTERM, and the Unix daemonize path (the Windows leg exercises the shared
// dispatch through the harness as before).

// shortTempDir returns a /tmp-rooted dir so AF_UNIX paths stay under the macOS
// sun_path limit (t.TempDir embeds the long test name).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// childToken's -token-file arm: the token is normalized like the reference
// (trailing newline stripped), the file is consumed, and the env vars that
// must not leak into spawned children are scrubbed.
func TestChildTokenFromFile(t *testing.T) {
	t.Setenv("CLAUDE_RPC_TOKEN", "ambient-client-token")
	t.Setenv(daemonChildEnv, "1")
	tf := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tf, []byte("tok-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := childToken(tf)
	if err != nil {
		t.Fatalf("childToken: %v", err)
	}
	if got != "tok-file" {
		t.Errorf("token = %q, want %q", got, "tok-file")
	}
	if _, err := os.Stat(tf); !os.IsNotExist(err) {
		t.Errorf("token file still present (err=%v), want consumed", err)
	}
	for _, env := range []string{"CLAUDE_RPC_TOKEN", daemonChildEnv} {
		if v, ok := os.LookupEnv(env); ok {
			t.Errorf("%s survived childToken as %q, want scrubbed", env, v)
		}
	}
}

// childToken's -token-fd arm: the token arrives over the inherited pipe fd
// named by tokenPipeEnv, which is scrubbed after the read.
func TestChildTokenFromPipeEnv(t *testing.T) {
	// A bare syscall fd with no competing *os.File owner — childToken's
	// readTokenFD closes it (see TestReadTokenFD for the ownership rule).
	tf := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tf, []byte("tok-pipe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(tf, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(tokenPipeEnv, strconv.Itoa(fd))

	got, err := childToken("ignored-when-pipe-set")
	if err != nil {
		t.Fatalf("childToken: %v", err)
	}
	if got != "tok-pipe" {
		t.Errorf("token = %q, want %q", got, "tok-pipe")
	}
	if v, ok := os.LookupEnv(tokenPipeEnv); ok {
		t.Errorf("%s survived childToken as %q, want scrubbed", tokenPipeEnv, v)
	}
}

// childToken's error arms: an unparsable pipe-fd env and a missing token file
// each produce the exact operator-facing prefix runServe prints.
func TestChildTokenErrors(t *testing.T) {
	t.Setenv(tokenPipeEnv, "not-a-number")
	if _, err := childToken(""); err == nil || !strings.Contains(err.Error(), "read token pipe") {
		t.Errorf("childToken bad pipe env = %v, want read token pipe error", err)
	}
	t.Setenv(tokenPipeEnv, "")
	if _, err := childToken(filepath.Join(t.TempDir(), "absent")); err == nil ||
		!strings.Contains(err.Error(), "read --token-file") {
		t.Errorf("childToken missing file = %v, want read --token-file error", err)
	}
}

// newServerOnSocket must reproduce the daemonized child's startup surface:
// a 0600 socket, the persisted daemon.token beside it, the optional metrics
// listener — and closeAll must retract all of it.
func TestNewServerOnSocketBootAndCloseAll(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")

	s, err := newServerOnSocket(sock, "boot-token", "127.0.0.1:0", false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.procs.killAll() })

	if fi, err := os.Stat(sock); err != nil {
		t.Fatalf("socket missing: %v", err)
	} else if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("socket mode = %o, want 0600", got)
	}
	tokenPath := filepath.Join(dir, "daemon.token")
	if b, err := os.ReadFile(tokenPath); err != nil || string(b) != "boot-token" {
		t.Errorf("daemon.token = %q (err %v), want boot-token", b, err)
	}
	if s.metricsLn == nil {
		t.Error("metrics listener not started for a valid -metrics-addr")
	}

	// The boot result must actually serve: a ping round-trips once the accept
	// loops start (still no os.Exit anywhere on this path).
	s.startAcceptLoops()
	cl := dial(t, sock)
	reply := string(cl.call(`{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"boot-token"}`))
	if !strings.Contains(reply, `"pong":true`) {
		t.Fatalf("ping = %s, want pong:true", reply)
	}

	s.signalShutdown()
	s.closeAll(sock)
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("socket survived closeAll (err=%v)", err)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Errorf("daemon.token survived closeAll (err=%v)", err)
	}
}

// A metrics bind failure is non-fatal (the daemon's job is the socket) and a
// socket bind failure is fatal — the two error arms around the boot.
func TestNewServerOnSocketErrorArms(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "m.sock")
	s, err := newServerOnSocket(sock, "tok", "127.0.0.1:99999999", false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket with bad metrics addr: %v", err)
	}
	if s.metricsLn != nil {
		t.Error("metrics listener present despite unbindable addr")
	}
	// A second listener standing in for the Windows named pipe: closeAll must
	// close it alongside the socket listener (the branch is listener-agnostic).
	pln, err := net.Listen("unix", filepath.Join(dir, "p.sock"))
	if err != nil {
		t.Fatal(err)
	}
	s.pipeLn = pln
	s.signalShutdown()
	s.closeAll(sock)
	if _, err := pln.Accept(); err == nil {
		t.Error("pipe stand-in listener still accepting after closeAll")
	}

	if _, err := newServerOnSocket(filepath.Join(dir, "absent", "x.sock"), "tok", "", false, false); err == nil ||
		!strings.Contains(err.Error(), "listen unix") {
		t.Errorf("newServerOnSocket into missing dir = %v, want listen unix error", err)
	}
}

// The detached child's two fatal arms: a token that cannot be obtained, and a
// socket that cannot be bound. Each must exit 1 (after the guards above,
// nothing else can stop a child boot).
func TestRunServeChildFatalArms(t *testing.T) {
	stubOsExit(t)
	t.Setenv(daemonChildEnv, "1")
	t.Setenv(tokenPipeEnv, "")
	dir := shortTempDir(t)

	code, exited := catchExit(func() {
		runServe(filepath.Join(dir, "s.sock"), filepath.Join(dir, "absent-token"), -1, "", false, false)
	})
	if !exited || code != 1 {
		t.Errorf("child with unreadable token: exited=%v code=%d, want exit 1", exited, code)
	}

	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", "/claustrum-no-such-shell") // keep extractLoginPATH inert
	code, exited = catchExit(func() {
		runServe(filepath.Join(dir, "no-dir", "s.sock"), tf, -1, "", false, false)
	})
	if !exited || code != 1 {
		t.Errorf("child with unbindable socket: exited=%v code=%d, want exit 1", exited, code)
	}
}

// runServe's fatal guards: no token source at all, and a parent whose
// -token-fd cannot be read. Both must exit 1 without daemonizing.
func TestRunServeFatalGuards(t *testing.T) {
	stubOsExit(t)
	t.Setenv(daemonChildEnv, "")

	if code, exited := catchExit(func() { runServe("s.sock", "", -1, "", false, false) }); !exited || code != 1 {
		t.Errorf("runServe without token source: exited=%v code=%d, want exit 1", exited, code)
	}
	// An fd number far past any open descriptor: readTokenFD wraps it but the
	// read fails (EBADF), so the parent dies before the re-exec.
	if code, exited := catchExit(func() { runServe("s.sock", "", 1<<20, "", false, false) }); !exited || code != 1 {
		t.Errorf("runServe with unreadable -token-fd: exited=%v code=%d, want exit 1", exited, code)
	}
}

// The parent half of -serve with -token-fd: read the caller's fd, re-exec
// detached (the child is this test binary steered into an exit-0 helper),
// forward the token over the inherited pipe, and exit 0.
func TestRunServeParentDaemonizes(t *testing.T) {
	stubOsExit(t)
	t.Setenv(daemonChildEnv, "")
	t.Setenv("CLAUSTRUM_TEST_HELPER", "exit:0")

	tf := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tf, []byte("fd-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(tf, syscall.O_RDONLY, 0) // bare fd; runServe's readTokenFD owns+closes it
	if err != nil {
		t.Fatal(err)
	}

	code, exited := catchExit(func() { runServe("unused.sock", "", fd, "", false, false) })
	if !exited || code != 0 {
		t.Errorf("parent runServe: exited=%v code=%d, want exit 0", exited, code)
	}
}

// daemonizeWithToken's -token-file shape (no forwarded token, no pipe): the
// re-exec'd child starts detached and the parent exits 0.
func TestDaemonizeWithoutForwardedToken(t *testing.T) {
	stubOsExit(t)
	t.Setenv("CLAUSTRUM_TEST_HELPER", "exit:0")
	if code, exited := catchExit(func() { daemonizeWithToken("") }); !exited || code != 0 {
		t.Errorf("daemonizeWithToken(\"\"): exited=%v code=%d, want exit 0", exited, code)
	}
}

// The full detached-child lifecycle, end to end and in-process: runServe boots
// off a -token-file, serves an authenticated ping, then a SIGTERM rides
// run()'s signal goroutine into teardown, which retracts the socket and
// daemon.token and exits 0.
func TestRunServeChildFullLifecycle(t *testing.T) {
	stubOsExit(t)
	t.Setenv(daemonChildEnv, "1")
	t.Setenv("CLAUDE_RPC_TOKEN", "ambient")
	// A shell that cannot exec keeps extractLoginPATH from rewriting the test
	// process's PATH (its failure path leaves PATH untouched).
	t.Setenv("SHELL", "/claustrum-no-such-shell")
	t.Cleanup(func() { signal.Reset(syscall.SIGTERM, syscall.SIGINT) })

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "d.sock")
	tf := filepath.Join(dir, "token")
	const token = "lifecycle-token"
	if err := os.WriteFile(tf, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	type exitResult struct {
		code   int
		exited bool
	}
	done := make(chan exitResult, 1)
	go func() {
		code, exited := catchExit(func() { runServe(sock, tf, -1, "", false, false) })
		done <- exitResult{code, exited}
	}()

	// Wait for the daemon to open the socket, then prove it authenticates.
	var nc net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		if nc, err = net.Dial("unix", sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon socket never came up: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cl := &testClient{t: t, nc: nc}
	go cl.readLoop()
	t.Cleanup(func() { _ = nc.Close() })
	reply := string(cl.call(`{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"` + token + `"}`))
	if !strings.Contains(reply, `"pong":true`) {
		t.Fatalf("ping = %s, want pong:true", reply)
	}
	if _, err := os.Stat(tf); !os.IsNotExist(err) {
		t.Errorf("token file survived boot (err=%v), want consumed", err)
	}

	// Graceful shutdown exactly as the OS delivers it.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("self SIGTERM: %v", err)
	}
	select {
	case r := <-done:
		if !r.exited || r.code != 0 {
			t.Errorf("daemon exit: exited=%v code=%d, want exit 0", r.exited, r.code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit after SIGTERM")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("socket survived teardown (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "daemon.token")); !os.IsNotExist(err) {
		t.Errorf("daemon.token survived teardown (err=%v)", err)
	}
}

// main's -serve dispatch wires the parent daemonize path end to end (config
// resolution included); the re-exec'd child is the exit-0 helper again.
func TestMainServeDispatch(t *testing.T) {
	t.Setenv(daemonChildEnv, "")
	t.Setenv("CLAUSTRUM_TEST_HELPER", "exit:0")
	dir := shortTempDir(t)
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("main-serve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, exited := runMain(t, "-serve", "-socket", filepath.Join(dir, "m.sock"), "-token-file", tf)
	if !exited || code != 0 {
		t.Errorf("main -serve: exited=%v code=%d, want exit 0", exited, code)
	}

	// Without -socket, main resolves the reference's default socket path — safe
	// to exercise here because the parent's daemonize half never touches the
	// socket (and the re-exec'd child is the exit-0 helper).
	if err := os.WriteFile(tf, []byte("main-serve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, exited = runMain(t, "-serve", "-token-file", tf)
	if !exited || code != 0 {
		t.Errorf("main -serve without -socket: exited=%v code=%d, want exit 0", exited, code)
	}
}
