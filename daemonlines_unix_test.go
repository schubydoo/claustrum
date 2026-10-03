//go:build unix

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The daemon lines of the run-dir lock and of the orphan self-exit. A Linux VM measured
// them against f6010b97 and 89cb6289 (rows ST02, ST09 and the start rows).

// TestClaimRunDirHeldByANonDaemon: a process that is no daemon holds daemon.lock, and the
// file holds no record (row ST02). The daemon signals nothing, serves without the lock and
// prints the three lines. This test holds the lock itself, so nothing is there to signal.
func TestClaimRunDirHeldByANonDaemon(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}
	oldHold := holdHolder
	t.Cleanup(func() { holdHolder = oldHold })
	signals := 0
	holdHolder = func(int) (func(syscall.Signal) error, func()) {
		return func(syscall.Signal) error { signals++; return syscall.ESRCH }, func() {}
	}
	buf := captureLogBuf(t)

	release := claimRunDir(sock, "serve").release
	release()

	wantLinesInOrder(t, buf.String(),
		"[daemon] serve: WARNING "+lockPath+` is held by a live process we will not signal (its holder is not a daemon (role "")); leaving it alone`,
		"[daemon] serve: previous owner of "+dir+": survivor",
		"[daemon] serve: not the run dir's lock holder; leaving any predecessor's children alone and recording none")
	if signals != 0 {
		t.Errorf("the holder got %d signal(s), want none", signals)
	}
	if fi, err := os.Stat(lockPath); err != nil || fi.Size() != 0 {
		t.Errorf("the lock file changed: %v, %v", fi, err)
	}
	// The test still holds the lock: the daemon did not take it.
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("the holder lost its lock: %v", err)
	}
}

// TestClaimRunDirHolderGetsNoNotHolderLine: a daemon that takes the lock does not print the
// "not the run dir's lock holder" line.
func TestClaimRunDirHolderGetsNoNotHolderLine(t *testing.T) {
	dir := shortTempDir(t)
	buf := captureLogBuf(t)
	release := claimRunDir(filepath.Join(dir, "rpc.sock"), "serve").release
	release()
	// Another test can leave a goroutine that logs into this buffer. Look for the line only.
	if got := buf.String(); strings.Contains(got, runDirNotHolderLine) {
		t.Errorf("log = %q, want no \"not the lock holder\" line from a daemon that holds the lock", got)
	}
}

// TestLockInstanceIsTheDaemonInstance: the instanceId in daemon.lock is the instanceId of
// server.capabilities and of the listening line. On a Linux VM the three held one value
// in 36 of 36 daemons of 89cb6289.
func TestLockInstanceIsTheDaemonInstance(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	s, err := newServerOnSocket(sock, "tok", "", wireLogOptions{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })
	rec, err := readOwnerRecord(filepath.Join(dir, runDirLockName))
	if err != nil {
		t.Fatalf("read owner record: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(rec.InstanceID) || rec.InstanceID != s.instanceID {
		t.Errorf("daemon.lock instanceId %q, daemon instance %q, want one 32-hex value", rec.InstanceID, s.instanceID)
	}
}

// TestOrphanExitLines: a daemon that lost its socket prints the reference's lines of its
// self-exit in order, the "shutdown requested" line and the cleanup line included (row ST09).
func TestOrphanExitLines(t *testing.T) {
	shrinkOrphanTimers(t, 10*time.Millisecond, 20*time.Millisecond)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	s, err := newServerOnSocket(sock, "orphan-tok", "", wireLogOptions{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.procs.killAll() })
	s.startAcceptLoops()
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	buf := captureLogBuf(t)
	done := make(chan struct{})
	go func() { s.exitWhenOrphaned(sock); close(done) }()
	select {
	case <-s.shutdown:
	case <-time.After(5 * time.Second):
		s.signalShutdown()
		<-done
		t.Fatal("the daemon that lost its socket did not shut down")
	}
	<-done
	s.closeAll(sock)

	got := buf.String()
	wantLinesInOrder(t, got,
		"[Server] socket path "+sock+" no longer leads to this daemon; shutting down if that holds for 20ms with nobody connected",
		"[Server] socket path "+sock+" did not lead back to this daemon (1/2); re-checking before acting")
	if !regexp.MustCompile(`\[Server\] orphaned for \S+ \(socket path gone or re-bound, 2 self-probes failed, no connections\); shutting down and killing 0 child process\(es\)\n\S+ +\[Server\] shutdown requested\n\S+ +\[Server\] cleanup: closed 0 connection\(s\), killed 0 child process group\(s\)\n$`).MatchString(got) {
		t.Errorf("log does not end with the orphaned line, the shutdown line and the cleanup line:\n%s", got)
	}
}
