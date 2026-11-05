//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/procinfo"
	"github.com/schubydoo/claustrum/internal/runlock"
	"github.com/schubydoo/claustrum/internal/wirelog"
)

// The tests of the run-dir lock through the start and the shutdown of a daemon. The
// lock itself is tested in internal/runlock.

func TestNewServerOnSocketWritesRunLock(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	s, err := newServerOnSocket(sock, "tok", "", wirelog.Options{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.procs.KillAll() })

	lockPath := filepath.Join(dir, runlock.LockName)
	rec, err := readOwnerRecord(lockPath)
	if err != nil {
		t.Fatalf("daemon.lock not written at boot: %v", err)
	}
	if rec.Pid != os.Getpid() || rec.Role != "serve" {
		t.Errorf("boot owner record = %+v, want our pid + role serve", rec)
	}

	s.signalShutdown()
	s.closeAll(sock)
	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("daemon.lock must survive closeAll (truncate, not unlink): %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("daemon.lock size after closeAll = %d, want 0", fi.Size())
	}
}

// shrinkLatePeriod sets a short period for the late tries and counts each try. Call it
// before the claim, so that its cleanup runs after the cleanup of the claim.
func shrinkLatePeriod(t *testing.T) *atomic.Int32 {
	t.Helper()
	var tries atomic.Int32
	oldPeriod := runlock.SetLatePeriodForTest(20 * time.Millisecond)
	oldFlock := runlock.SetLateFlockForTest(func(fd, how int) error {
		tries.Add(1)
		return syscall.Flock(fd, how)
	})
	t.Cleanup(func() { runlock.SetLatePeriodForTest(oldPeriod); runlock.SetLateFlockForTest(oldFlock) })
	return &tries
}

// lateLines returns the late lines of the capture that name instance.
func lateLines(buf *syncBuffer, instance string) []string {
	var out []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "run-dir lock acquired late") && strings.Contains(line, instance) {
			out = append(out, line)
		}
	}
	return out
}

// lockIsHeld reports whether another descriptor holds the flock on path. A free lock is
// dropped again at once.
func lockIsHeld(t *testing.T, path string) bool {
	t.Helper()
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer syscall.Close(fd)
	return syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) == syscall.EWOULDBLOCK
}

// pollUntil polls cond every 5 ms until it holds or limit passes.
func pollUntil(limit time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(limit)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestLateRunDirLockAfterHolderLetsGo follows two cases of 5fd08069 (measured on Linux and
// macOS, and on Linux). A start finds daemon.lock held by a process that is not a
// daemon and serves without the lock. After the holder lets go, a later try takes the lock, logs
// the late line once and writes the full record with the startedAt of the start. The daemon holds
// the lock until its shutdown, which leaves an empty file.
func TestLateRunDirLockAfterHolderLetsGo(t *testing.T) {
	buf := captureLogBuf(t)
	tries := shrinkLatePeriod(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	lockPath := filepath.Join(dir, runlock.LockName)
	letGo := startEmptyLockHolder(t, lockPath)

	s, err := newServerOnSocket(sock, "tok", "", wirelog.Options{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.releaseRunDir(); s.procs.KillAll() })
	if !strings.Contains(buf.String(), runlock.NotHolderLine) {
		t.Fatalf("log = %q, want the start without the lock", buf.String())
	}

	letGo()
	if !pollUntil(5*time.Second, func() bool { return len(lateLines(buf, s.instanceID)) > 0 }) {
		t.Fatalf("no late line after the holder let go; log = %q", buf.String())
	}
	want := fmt.Sprintf("[daemon] run-dir lock acquired late (previous holder gone); recorded pid %d (instance %q)", os.Getpid(), s.instanceID)
	if got := lateLines(buf, s.instanceID)[0]; !strings.HasSuffix(got, want) {
		t.Errorf("late line = %q, want it to end with %q", got, want)
	}

	rec, err := json.Marshal(runlock.OwnerRecord{Pid: os.Getpid(), Role: "serve", Node: procinfo.NodeID(), InstanceID: s.instanceID, StartedAt: s.startedAt})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(rec)+"\n" {
		t.Errorf("daemon.lock after the late try = %q, want %q", got, string(rec)+"\n")
	}
	if !lockIsHeld(t, lockPath) {
		t.Error("the daemon does not hold daemon.lock after the late line")
	}
	if !s.procs.NoRecords {
		t.Error("a late lock changed the decision of the start about child records")
	}

	// A good try ends the tries: no second late line (measured on Linux).
	after := tries.Load()
	if pollUntil(20*runlock.LatePeriodForTest(), func() bool { return tries.Load() > after }) {
		t.Errorf("the daemon tried the lock again after a late try took it (%d tries, then %d)", after, tries.Load())
	}
	if n := len(lateLines(buf, s.instanceID)); n != 1 {
		t.Errorf("%d late lines, want 1", n)
	}

	s.signalShutdown()
	s.closeAll(sock)
	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("daemon.lock must stay after the shutdown: %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("daemon.lock is %d bytes after the shutdown of a late holder, want 0", fi.Size())
	}
	if lockIsHeld(t, lockPath) {
		t.Error("daemon.lock is still held after the shutdown of a late holder")
	}
}

// TestLateRunDirLockStopsAtShutdown pins claustrum's own rule: no try takes the lock
// once the shutdown began, also when the holder lets go after that.
func TestLateRunDirLockStopsAtShutdown(t *testing.T) {
	buf := captureLogBuf(t)
	tries := shrinkLatePeriod(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	lockPath := filepath.Join(dir, runlock.LockName)
	letGo := startEmptyLockHolder(t, lockPath)

	s, err := newServerOnSocket(sock, "tok", "", wirelog.Options{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.releaseRunDir(); s.procs.KillAll() })

	s.signalShutdown()
	letGo()
	at := tries.Load()
	if pollUntil(20*runlock.LatePeriodForTest(), func() bool { return tries.Load() > at }) {
		t.Errorf("a late try ran after the shutdown began (%d tries, then %d)", at, tries.Load())
	}
	if lines := lateLines(buf, s.instanceID); len(lines) != 0 {
		t.Errorf("late lines after the shutdown began: %q", lines)
	}
	if lockIsHeld(t, lockPath) {
		t.Error("the daemon took daemon.lock after its shutdown began")
	}
	s.closeAll(sock)
}
