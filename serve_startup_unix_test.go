//go:build unix

package main

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// waitForDaemonAccept must return as soon as the socket accepts a connection.
func TestWaitForDaemonAcceptReturnsWhenListening(t *testing.T) {
	dir, err := os.MkdirTemp("", "wda")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	done := make(chan bool, 1)
	go func() { ok, _ := waitForDaemonAccept(sock, 0); done <- ok }()
	select {
	case ok := <-done:
		if !ok {
			t.Error("waitForDaemonAccept reported failure for a listening socket")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waitForDaemonAccept did not return for a listening socket")
	}
}

// A folder with a file at the socket path does not end the wait: no connect works
// there, so the launcher gives up at its bound. 5fd08069 exited 1 after about 12 s
// with the timeout text (round task006-01, cell C9n Linux VM, cell C9 macOS VM).
func TestWaitForDaemonAcceptWaitsOverAFolder(t *testing.T) {
	old := daemonStartTimeout
	daemonStartTimeout = 300 * time.Millisecond
	t.Cleanup(func() { daemonStartTimeout = old })

	occupied := filepath.Join(shortTempDir(t), "rpc.sock")
	if err := os.Mkdir(occupied, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "keep.txt"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	ok, saw := waitForDaemonAccept(occupied, 0)
	if ok || saw {
		t.Errorf("waitForDaemonAccept = (%v, %v) for a folder at the path, want (false, false)", ok, saw)
	}
	if el := time.Since(start); el < 250*time.Millisecond {
		t.Errorf("returned after %v, want the %v bound: the folder ended the wait", el, daemonStartTimeout)
	}
}

// With neither a listener nor a child exit, the wait is bounded by
// daemonStartTimeout rather than blocking forever.
func TestWaitForDaemonAcceptGivesUpAtDeadline(t *testing.T) {
	old := daemonStartTimeout
	daemonStartTimeout = 250 * time.Millisecond
	t.Cleanup(func() { daemonStartTimeout = old })

	start := time.Now()
	if ok, _ := waitForDaemonAccept(filepath.Join(shortTempDir(t), "never-exists.sock"), 0); ok {
		t.Error("waitForDaemonAccept = true for a socket that never appears, want false")
	}
	el := time.Since(start)
	if el < 200*time.Millisecond {
		t.Errorf("returned after %v, want at least the %v deadline", el, daemonStartTimeout)
	}
	if el > 5*time.Second {
		t.Errorf("returned after %v, want bounded by the %v deadline", el, daemonStartTimeout)
	}
}

// A failed start is NOT silent: the launcher prints the timeout line to stderr
// and exits 1. docs/DAEMON.md → "The launcher wait" quotes that line as the
// contract, and the exit code
// alone was already covered — this pins the message text so the documented
// string cannot drift away from the code.
//
// The reference reports exactly this and exits 1 (measured at 5db5e4a with a
// zero-byte -token-file, whose child refuses to start): "claude-ssh: timeout
// waiting for daemon to accept on <socket>", after the full deadline. Only the
// program name differs.
func TestDaemonizeTimeoutPrintsToStderr(t *testing.T) {
	shortDaemonStart(t)
	stubOsExit(t)
	t.Setenv("CLAUSTRUM_TEST_HELPER", "exit:0") // a child that never binds

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	sock := filepath.Join(shortTempDir(t), "never.sock")
	code, exited := catchExit(func() { daemonizeWithToken(sock, "") })
	_ = w.Close()
	os.Stderr = old

	if !exited || code != 1 {
		t.Errorf("daemonizeWithToken: exited=%v code=%d, want exit 1", exited, code)
	}
	out, _ := io.ReadAll(r)
	want := "claustrum: timeout waiting for daemon to accept on " + sock
	if !strings.Contains(string(out), want) {
		t.Errorf("stderr = %q, want it to contain %q", out, want)
	}
}

// The OTHER failure arm of the same wait, and the one the exit code cannot tell
// apart: when a LIVE predecessor still owns the socket and the child never takes
// the path over, the launcher reports a distinct "did not take over"
// line instead of the accept timeout above. Both exit 1, so only the message
// discriminates them.
//
// The predecessor here is a plain listener of the test process, and the stub child
// never rebinds the path. So each connect of the wait reaches the pid that
// livePredecessorPID read. 5fd08069 prints this text in that state (round
// task006-01, cell C4, Linux and macOS VMs).
func TestDaemonizePredecessorHandoffPrintsToStderr(t *testing.T) {
	shortDaemonStart(t)
	stubOsExit(t)
	t.Setenv("CLAUSTRUM_TEST_HELPER", "exit:0") // a child that never rebinds the path

	dir, err := os.MkdirTemp("", "dpr") // short path: macOS sun_path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	code, exited := catchExit(func() { daemonizeWithToken(sock, "") })
	_ = w.Close()
	os.Stderr = old

	if !exited || code != 1 {
		t.Errorf("daemonizeWithToken: exited=%v code=%d, want exit 1", exited, code)
	}
	out, _ := io.ReadAll(r)
	want := "claustrum: daemon did not take over " + sock
	if !strings.Contains(string(out), want) {
		t.Errorf("stderr = %q, want it to contain %q", out, want)
	}
}

// The stale socket file of a killed daemon is not the socket of the new one: the
// launcher waits past it, and exits 0 when a daemon accepts on the path. Measured on
// Linux against f6010b97 and 89cb6289 (row XA): the -serve command returned 2.06 s
// after its start, when the new daemon had ended a recorded child and bound. The
// helper child here never binds, so the test binds the path itself after a pause.
func TestDaemonizeWaitsPastAStaleSocketFile(t *testing.T) {
	old := daemonStartTimeout
	daemonStartTimeout = 10 * time.Second
	t.Cleanup(func() { daemonStartTimeout = old })
	stubOsExit(t)
	t.Setenv("CLAUSTRUM_TEST_HELPER", "exit:0")

	dir, err := os.MkdirTemp("", "doc") // short path: macOS sun_path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "rpc.sock")
	staleSocketFile(t, sock)

	const bindAfter = 300 * time.Millisecond
	bound := make(chan net.Listener, 1)
	start := time.Now()
	go func() {
		time.Sleep(bindAfter)
		_ = os.Remove(sock)
		ln, _ := net.Listen("unix", sock)
		bound <- ln
	}()
	code, exited := catchExit(func() { daemonizeWithToken(sock, "") })
	took := time.Since(start)
	if ln := <-bound; ln != nil {
		_ = ln.Close()
	}
	if !exited || code != 0 {
		t.Errorf("daemonizeWithToken over a stale socket that a daemon then binds: exited=%v code=%d, want exit 0", exited, code)
	}
	if took < bindAfter {
		t.Errorf("the launcher returned after %v, want %v or more: it took the stale socket file for the new socket", took, bindAfter)
	}
}

// With a stale socket file and a child that never binds, the launcher gives up at its
// deadline with the timeout line and exit 1. That outcome is claustrum's own and is
// not measured: no row has a stale socket file and a daemon that never binds.
func TestDaemonizeTimesOutOnAStaleSocketFile(t *testing.T) {
	shortDaemonStart(t)
	stubOsExit(t)
	t.Setenv("CLAUSTRUM_TEST_HELPER", "exit:0")

	dir, err := os.MkdirTemp("", "doc") // short path: macOS sun_path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "rpc.sock")
	staleSocketFile(t, sock)

	if code, exited := catchExit(func() { daemonizeWithToken(sock, "") }); !exited || code != 1 {
		t.Errorf("daemonizeWithToken over a stale socket that nothing binds: exited=%v code=%d, want exit 1", exited, code)
	}
}

// -serve must create a missing socket directory (0700) and come back only once
// the daemon is accepting. Measured against the reference at 5db5e4a: it leaves
// d sub(700) / rpc.sock(600) / daemon.token(600) / remote-server.log(600) and the
// socket is already present the instant the launcher returns. claustrum used to
// refuse to start at all when the directory was absent, and when it did start it
// returned before the socket existed.
func TestServeCreatesSocketDirAndWaitsForAccept(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the real binary; skipped under -short")
	}
	bin := buildClaustrum(t)

	// Short base path keeps the AF_UNIX path under the macOS sun_path limit.
	base, err := os.MkdirTemp("", "svs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	sub := filepath.Join(base, "sub") // deliberately absent
	sock := filepath.Join(sub, "rpc.sock")
	tokFile := filepath.Join(base, "tok")
	if err := os.WriteFile(tokFile, []byte("tok-serve-startup"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "-serve", "-socket", sock, "-token-file", tokFile)
	cmd.Env = append(os.Environ(), "CLAUDE_SSH_DAEMON_CHILD=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("-serve launcher failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if c, err := net.Dial("unix", sock); err == nil {
			_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"server.shutdown","auth":"tok-serve-startup"}` + "\n"))
			_ = c.Close()
		}
	})

	// The launcher has returned. The socket must ALREADY be connectable — no
	// polling here, that is the whole point of the assertion.
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("socket not accepting at the instant -serve returned: %v", err)
	}
	_ = c.Close()

	fi, err := os.Stat(sub)
	if err != nil {
		t.Fatalf("socket directory was not created: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("socket dir mode = %04o, want 0700", got)
	}
}

// countAccepts accepts on ln until it closes, and counts each connection. Each
// connection stays open until the client closes it, so that the peer pid of a
// live peer is read (on macOS a closed peer may give none, not measured).
func countAccepts(ln net.Listener) *atomic.Int32 {
	var n atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			go func(c net.Conn) {
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}(c)
		}
	}()
	return &n
}

// A socket file that is bound but does not accept yet does not end the wait. On
// 5fd08069 with a listen delayed 0.5 s, a client connect right after the launcher exit
// worked 10 of 10 times (round task006-01, cell C7b, Linux VM).
func TestWaitForDaemonAcceptWaitsForTheListen(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "rpc.sock")
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: sock}); err != nil {
		t.Fatal(err)
	}

	done := make(chan bool, 1)
	go func() { ok, _ := waitForDaemonAccept(sock, 0); done <- ok }()
	select {
	case <-done:
		t.Fatal("the wait ended on a socket file that does not accept yet")
	case <-time.After(300 * time.Millisecond):
	}
	if err := syscall.Listen(fd, 8); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Error("the wait gave up although the socket listens")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end after the listen")
	}
}

// A new daemon's socket counts as new when it has the device and inode of the socket
// at the start. On ext4 the new socket of 5fd08069 kept the inode of the old one in
// 40 of 40 take-overs, and its launcher exited 0 each time (round task006-01, cell
// C3, Linux VM). Here the socket at the start and the one that answers are one file.
// The recorded predecessor is the pid of the parent process, which does not listen.
func TestWaitForDaemonAcceptTakesANewSocketWithTheOldInode(t *testing.T) {
	old := daemonStartTimeout
	daemonStartTimeout = 3 * time.Second
	t.Cleanup(func() { daemonStartTimeout = old })
	sock := filepath.Join(shortTempDir(t), "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	countAccepts(ln)
	before, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	pred := os.Getppid()
	if pred == os.Getpid() || pred <= 1 {
		t.Skipf("no usable parent pid (%d)", pred)
	}

	ok, saw := waitForDaemonAccept(sock, pred)
	if !ok || saw {
		t.Errorf("waitForDaemonAccept = (%v, %v), want (true, false): the old inode hid the new daemon", ok, saw)
	}
	after, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the socket at the path changed: the test did not stage one inode")
	}
}

// While the predecessor answers, the launcher connects about every 50 ms until its
// bound, and then reports the predecessor. 5fd08069 made 234 to 240 connects to the
// predecessor in its 12 s wait (round task006-01, cell C4, Linux and macOS VMs).
func TestWaitForDaemonAcceptConnectsEveryPoll(t *testing.T) {
	old := daemonStartTimeout
	daemonStartTimeout = 600 * time.Millisecond
	t.Cleanup(func() { daemonStartTimeout = old })

	sock := filepath.Join(shortTempDir(t), "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	n := countAccepts(ln)

	ok, saw := waitForDaemonAccept(sock, os.Getpid())
	if ok || !saw {
		t.Errorf("waitForDaemonAccept = (%v, %v) with only the predecessor, want (false, true)", ok, saw)
	}
	// 600 ms at 50 ms is 12 or 13 connects. A slow runner makes fewer, so the floor
	// is low, and the old wait made 1.
	for limit := time.Now().Add(2 * time.Second); n.Load() < 4 && time.Now().Before(limit); {
		time.Sleep(10 * time.Millisecond)
	}
	if got := n.Load(); got < 4 {
		t.Errorf("the predecessor got %d connects in the wait, want 4 or more", got)
	}
}

// A predecessor that answers before the child starts and then goes, with no new
// daemon, gives the timeout text: no connect of the wait reached the predecessor.
// That choice is claustrum's own, not measured. The child start is seamed, and it
// closes the predecessor.
func TestDaemonizeTimeoutTextWhenThePredecessorGoes(t *testing.T) {
	shortDaemonStart(t)
	stubOsExit(t)

	sock := filepath.Join(shortTempDir(t), "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	countAccepts(ln)
	oldStart := startDaemonChild
	startDaemonChild = func(*exec.Cmd) error { return ln.Close() }
	t.Cleanup(func() { startDaemonChild = oldStart })

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	code, exited := catchExit(func() { daemonizeWithToken(sock, "") })
	_ = w.Close()
	os.Stderr = old

	if !exited || code != 1 {
		t.Errorf("daemonizeWithToken: exited=%v code=%d, want exit 1", exited, code)
	}
	out, _ := io.ReadAll(r)
	want := "claustrum: timeout waiting for daemon to accept on " + sock
	if !strings.Contains(string(out), want) {
		t.Errorf("stderr = %q, want it to contain %q", out, want)
	}
}

// With a predecessor pid on record, a connect whose peer pid cannot be read does not
// end the wait. That rule is claustrum's own, not measured. The peer pid read is seamed to give 0.
func TestWaitForDaemonAcceptGoesOnAtAnUnknownPeerPID(t *testing.T) {
	old := daemonStartTimeout
	daemonStartTimeout = 300 * time.Millisecond
	t.Cleanup(func() { daemonStartTimeout = old })
	oldPeer := daemonPeerPID
	daemonPeerPID = func(net.Conn) int { return 0 }
	t.Cleanup(func() { daemonPeerPID = oldPeer })

	sock := filepath.Join(shortTempDir(t), "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	n := countAccepts(ln)

	start := time.Now()
	ok, saw := waitForDaemonAccept(sock, os.Getpid())
	if ok || saw {
		t.Errorf("waitForDaemonAccept = (%v, %v) at an unknown peer pid, want (false, false)", ok, saw)
	}
	if el := time.Since(start); el < 250*time.Millisecond {
		t.Errorf("returned after %v, want the %v bound", el, daemonStartTimeout)
	}
	if n.Load() < 1 {
		t.Error("no connect reached the listener")
	}
}

// With no predecessor pid, the first connect that works ends the wait, also when its
// peer pid cannot be read. That is the Windows rule (round task006-01, cell C3,
// Windows VM).
func TestWaitForDaemonAcceptUnknownPredecessorEndsAtFirstConnect(t *testing.T) {
	old := daemonStartTimeout
	daemonStartTimeout = 2 * time.Second
	t.Cleanup(func() { daemonStartTimeout = old })
	oldPeer := daemonPeerPID
	daemonPeerPID = func(net.Conn) int { return 0 }
	t.Cleanup(func() { daemonPeerPID = oldPeer })

	sock := filepath.Join(shortTempDir(t), "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	countAccepts(ln)
	if ok, saw := waitForDaemonAccept(sock, 0); !ok || saw {
		t.Errorf("waitForDaemonAccept = (%v, %v), want (true, false)", ok, saw)
	}
}
