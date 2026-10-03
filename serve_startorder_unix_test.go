//go:build linux || darwin

package main

import (
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// staleSocketFile leaves a socket file at path that nothing listens on, as a killed
// daemon does.
func staleSocketFile(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
}

// TestStartReapsBeforeItBinds pins the order of a start (89cb6289, Linux and macOS
// rows D1a, D1b, D3, XA and XL). A dead daemon left one recorded child that ignores
// SIGTERM. While the new daemon waits for that child, nothing listens: a client gets
// ECONNREFUSED from the stale socket file, or ENOENT when there is none. In that wait
// daemon.lock holds pid, role and node only. After the start the lock holds the
// instanceId and the startedAt that server.capabilities answers. Every signal and
// every read of a process is seamed.
func TestStartReapsBeforeItBinds(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	for _, tc := range []struct {
		name  string
		stale bool
		want  error
	}{
		{"D1a a stale socket file", true, syscall.ECONNREFUSED},
		{"D1b no socket file", false, syscall.ENOENT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortTempDir(t)
			sock := filepath.Join(dir, "rpc.sock")
			lock := filepath.Join(dir, runDirLockName)
			if tc.stale {
				staleSocketFile(t, sock)
			}
			const pid = 800000301
			name := writeRec(t, dir, orphanRecord(pid, "/f/bin/sigstub", ""))
			sim := newProcSim(t)
			sim.live[pid] = &liveProc{state: procAlive, startTicks: "9000", pgid: pid,
				program: "/f/bin/sigstub", runDir: dir, childMark: strconv.Itoa(pid) + ":9000"}
			sim.diesOn(pid, syscall.SIGKILL)

			// The first sleep of the reap is inside its wait for the group.
			var probed bool
			var dialErr error
			var lockInWait []byte
			var recordInWait bool
			var waitedAt int64
			simSleep := reapSleep
			reapSleep = func(d time.Duration) {
				if !probed {
					probed = true
					c, err := net.Dial("unix", sock)
					if err == nil {
						_ = c.Close()
					}
					dialErr = err
					lockInWait, _ = os.ReadFile(lock)
					recordInWait = recordExists(dir, name)
					// Real time passes in the wait, so a startedAt from before the
					// reap is lower than the time that is read here.
					time.Sleep(5 * time.Millisecond)
					waitedAt = time.Now().UnixMilli()
				}
				simSleep(d)
			}

			s, err := newServerOnSocket(sock, "tok", "", wireLogOptions{}, false, false)
			if err != nil {
				t.Fatalf("newServerOnSocket: %v", err)
			}
			t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })

			if !probed {
				t.Fatal("the reap of the start did not wait for the recorded child")
			}
			if !errors.Is(dialErr, tc.want) {
				t.Errorf("a dial in the wait gave %v, want %v", dialErr, tc.want)
			}
			wantShort := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"role":"serve","node":"` + nodeID() + `"}` + "\n"
			if string(lockInWait) != wantShort {
				t.Errorf("daemon.lock in the wait = %q, want %q", lockInWait, wantShort)
			}
			if !recordInWait {
				t.Error("the record of the signalled child was gone in the wait")
			}
			if sim.sigCount(syscall.SIGTERM) != 1 || sim.sigCount(syscall.SIGKILL) != 1 {
				t.Errorf("kills = %v, want SIGTERM and then SIGKILL to the group", sim.kills)
			}

			rec, err := readOwnerRecord(lock)
			if err != nil {
				t.Fatalf("read owner record: %v", err)
			}
			if rec.InstanceID != s.instanceID || rec.StartedAt != s.startedAt {
				t.Errorf("daemon.lock holds instanceId %q and startedAt %d, the daemon answers %q and %d",
					rec.InstanceID, rec.StartedAt, s.instanceID, s.startedAt)
			}
			if s.startedAt < waitedAt {
				t.Errorf("startedAt %d is from before the wait of the reap (%d), want it from after the reap", s.startedAt, waitedAt)
			}
			c, err := net.Dial("unix", sock)
			if err != nil {
				t.Fatalf("a dial after the start failed: %v", err)
			}
			_ = c.Close()
		})
	}
}

// TestRunServeHandlesASignalDuringItsStart pins Linux row P5 (89cb6289). A SIGTERM comes
// while the daemon waits in the reap of its start. The daemon logs the "received" line
// at once and does not end. It finishes the reap, binds, logs "shutdown requested",
// prints the listening line, cleans up and exits 0. The signal goes through the
// notifySignals seam, and every signal and every read of a process is seamed.
func TestRunServeHandlesASignalDuringItsStart(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	stubOsExit(t)
	t.Setenv(daemonChildEnv, "1")
	t.Setenv(tokenPipeEnv, "")
	stubLoginPATHExtractor(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const pid = 800000401
	writeRec(t, dir, orphanRecord(pid, "/f/bin/sigstub", ""))
	sim := newProcSim(t)
	sim.live[pid] = &liveProc{state: procAlive, startTicks: "9000", pgid: pid,
		program: "/f/bin/sigstub", runDir: dir, childMark: strconv.Itoa(pid) + ":9000"}
	sim.diesOn(pid, syscall.SIGKILL)

	// The log and the listening line go to one pipe, so one text holds their order.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var all syncBuffer
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(&all, r); close(copied) }()
	oldOut, oldW, oldF := os.Stdout, log.Writer(), log.Flags()
	os.Stdout = w
	log.SetOutput(w)
	log.SetFlags(0)
	restore := sync.OnceFunc(func() {
		os.Stdout = oldOut
		log.SetOutput(oldW)
		log.SetFlags(oldF)
		_ = w.Close()
		<-copied
		_ = r.Close()
	})
	t.Cleanup(restore)

	var sigc chan<- os.Signal
	oldNotify := notifySignals
	notifySignals = func(c chan<- os.Signal, _ ...os.Signal) { sigc = c }
	t.Cleanup(func() { notifySignals = oldNotify })

	// The first sleep of the reap is inside its wait for the group.
	const received = "[daemon] received terminated; shutting down (children will be killed)"
	var sent, noHandler, noLine bool
	var dialErr error
	simSleep := reapSleep
	reapSleep = func(d time.Duration) {
		if !sent {
			sent = true
			if sigc == nil {
				// End the start here, so a daemon with no handler does not serve on.
				noHandler = true
				panic(exitPanic{99})
			}
			sigc <- syscall.SIGTERM
			deadline := time.Now().Add(10 * time.Second)
			for !strings.Contains(all.String(), received) {
				if time.Now().After(deadline) {
					noLine = true
					break
				}
				time.Sleep(time.Millisecond)
			}
			c, err := net.Dial("unix", sock)
			if err == nil {
				_ = c.Close()
			}
			dialErr = err
		}
		simSleep(d)
	}

	// A daemon that does not stop after the signal gets a server.shutdown request, so a
	// broken build fails this test and does not hang it.
	var forced atomic.Bool
	watchdog := time.AfterFunc(20*time.Second, func() {
		forced.Store(true)
		if c, err := net.Dial("unix", sock); err == nil {
			_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"server.shutdown","params":{}}` + "\n"))
			_, _ = io.Copy(io.Discard, c)
			_ = c.Close()
		}
	})
	code, exited := catchExit(func() { runServe(sock, tf, -1, "", wireLogOptions{}, false, false) })
	watchdog.Stop()
	restore()

	if forced.Load() {
		t.Fatal("the daemon did not stop within 20 s of the signal")
	}
	if noHandler {
		t.Fatal("no signal handler was in place at the reap of the start")
	}
	if !sent {
		t.Fatal("the reap of the start did not wait for the recorded child")
	}
	if noLine {
		t.Errorf("no %q line in the wait of the reap", received)
	}
	if !errors.Is(dialErr, syscall.ENOENT) {
		t.Errorf("a dial after the signal, in the wait, gave %v, want ENOENT: the start goes on and nothing listens yet", dialErr)
	}
	if !exited || code != 0 {
		t.Errorf("daemon exit: exited=%v code=%d, want exit 0", exited, code)
	}
	if sim.sigCount(syscall.SIGTERM) != 1 || sim.sigCount(syscall.SIGKILL) != 1 {
		t.Errorf("kills = %v, want SIGTERM and then SIGKILL to the group", sim.kills)
	}
	wantLogOrder(t, all.String(),
		"[process.Registry] ending orphaned process group "+strconv.Itoa(pid),
		received,
		"[process.Registry] group "+strconv.Itoa(pid)+" outlived SIGTERM for 2s: SIGKILL",
		"1 orphaned group(s): 0 ended on SIGTERM, 1 on SIGKILL, 0 survived",
		"[Server] shutdown requested",
		"remote server listening on "+sock,
		"[Server] cleanup: closed 0 connection(s), killed 0 child process group(s)",
	)
	for _, name := range []string{"rpc.sock", "daemon.token"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s is still there after the exit (err=%v)", name, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, runDirLockName)); err != nil || len(b) != 0 {
		t.Errorf("daemon.lock after the exit = %q (err=%v), want an empty file", b, err)
	}
}

// TestSignalWatchHandlesOneSignal pins Linux row H8c (89cb6289): of two SIGTERM in the
// start, the daemon handles one. It logs one "received" line, and the attach of the
// server then logs one "shutdown requested" line. The second signal stays unread.
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

// TestLauncherWaitsPastAStaleSocketFile pins the launcher side of that order (Linux
// row XA): with the stale socket file of a killed daemon on disk, the -serve command
// returns when the new daemon accepts, not at once.
func TestLauncherWaitsPastAStaleSocketFile(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	staleSocketFile(t, sock)
	stale := staleSocketIdent(sock)
	if stale == nil {
		t.Fatal("staleSocketIdent gave nil for a socket file")
	}

	const bindAfter = 300 * time.Millisecond
	bound := make(chan net.Listener, 1)
	start := time.Now()
	go func() {
		time.Sleep(bindAfter)
		_ = os.Remove(sock)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			bound <- nil
			return
		}
		bound <- ln
	}()
	ok := waitForDaemonAccept(sock, nil, stale)
	took := time.Since(start)
	if ln := <-bound; ln != nil {
		_ = ln.Close()
	}
	if !ok {
		t.Fatal("waitForDaemonAccept gave up")
	}
	if took < bindAfter {
		t.Errorf("the wait took %v, want %v or more: it returned on the stale socket file", took, bindAfter)
	}
}

// TestStaleSocketIdentIgnoresOtherKinds pins that only a socket file makes the
// launcher wait: a directory at the socket path gives nil, so the wait for it stays as
// measured (the launcher returns at once).
func TestStaleSocketIdentIgnoresOtherKinds(t *testing.T) {
	dir := shortTempDir(t)
	if fi := staleSocketIdent(dir); fi != nil {
		t.Errorf("staleSocketIdent of a directory = %v, want nil", fi.Name())
	}
	if fi := staleSocketIdent(filepath.Join(dir, "absent.sock")); fi != nil {
		t.Errorf("staleSocketIdent of a missing path = %v, want nil", fi.Name())
	}
}
