//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// holdStopRecord takes the flock on daemon.lock of dir through its own open of the
// file and writes a record with role "stop". It returns the descriptor. A flock of
// another open of the file conflicts with it, also in this process.
func holdStopRecord(t *testing.T, dir string) int {
	t.Helper()
	fd, err := syscall.Open(filepath.Join(dir, runDirLockName), syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	writeOwnerRecord(fd, ownerRecord{Pid: 999999, Role: "stop", Node: nodeID()})
	return fd
}

// A new daemon tries the lock of a "stop" holder again for the grace before it logs
// the "has not let go" line and goes on without the lock (cells L2 and L3 of round
// task091-01, Linux and macOS VMs).
func TestClaimRunDirWaitsForStopHolder(t *testing.T) {
	old := runDirStopGrace
	runDirStopGrace = 300 * time.Millisecond
	t.Cleanup(func() { runDirStopGrace = old })
	dir := shortTempDir(t)
	fd := holdStopRecord(t, dir)
	t.Cleanup(func() { _ = syscall.Close(fd) })

	buf := captureLogBuf(t)
	start := time.Now()
	claim := lockRunFolder(filepath.Join(dir, "s.sock"), "serve")
	took := time.Since(start)
	t.Cleanup(claim.release)

	if took < runDirStopGrace {
		t.Errorf("the claim took %s, want at least the grace %s of tries", took, runDirStopGrace)
	}
	if claim.holder {
		t.Error("the claim holds the lock of a stop holder that kept it")
	}
	if got := buf.String(); !strings.Contains(got, "is held by a --stop (pid 999999) that has not let go; leaving it") {
		t.Errorf("log misses the has-not-let-go line\n--- got ---\n%s", got)
	}
}

// A "stop" holder that lets go during the tries leaves the lock to the new daemon.
// That arm is claustrum's own and not measured.
func TestClaimRunDirTakesLockAStopHolderReleases(t *testing.T) {
	old := runDirStopGrace
	runDirStopGrace = 5 * time.Second
	t.Cleanup(func() { runDirStopGrace = old })
	dir := shortTempDir(t)
	fd := holdStopRecord(t, dir)
	timer := time.AfterFunc(100*time.Millisecond, func() { _ = syscall.Close(fd) })
	t.Cleanup(func() { timer.Stop() })

	buf := captureLogBuf(t)
	claim := lockRunFolder(filepath.Join(dir, "s.sock"), "serve")
	t.Cleanup(claim.release)

	if !claim.holder {
		t.Error("the claim did not take the lock that the stop holder let go")
	}
	if got := buf.String(); strings.Contains(got, "has not let go") {
		t.Errorf("log holds the has-not-let-go line for a holder that let go\n--- got ---\n%s", got)
	}
}
