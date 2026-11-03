//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/runlock"
)

// The lock-holder fixtures of the tests of package main that drive -stop and the
// start of a daemon. internal/runlock has its own copies for its own tests.

// readOwnerRecord reads the owner record at path, as the daemon does.
func readOwnerRecord(path string) (ownerRecord, error) { return runlock.ReadOwnerRecordForTest(path) }

// writeOwnerRecord writes rec into the locked fd, as the daemon does.
func writeOwnerRecord(fd int, rec ownerRecord) { runlock.WriteOwnerRecordForTest(fd, rec) }

// runlockHoldFixture is the "runlock-hold" helper mode: take the flock on args[0],
// write an owner record naming this process a serve daemon, then linger so lockRunFolder
// must evict it. args[1] == "term-ignore" swallows SIGTERM (the escalation case);
// args[2] is a ready-file the parent waits on; an optional args[3] overrides the node
// so the parent can exercise the machine-identity refusal guard.
func runlockHoldFixture(args []string) int {
	lockPath, termMode, readyPath := args[0], args[1], args[2]
	fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		return 1
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(os.Stderr, "flock:", err)
		return 1
	}
	node := nodeID()
	if len(args) > 3 && args[3] != "" {
		node = args[3]
	}
	writeOwnerRecord(fd, ownerRecord{
		Pid:        os.Getpid(),
		Role:       "serve",
		Node:       node,
		InstanceID: newDaemonInstanceID(),
		StartedAt:  time.Now().UnixMilli(),
	})
	if termMode == "term-ignore" {
		ignoreSigterm()
	}
	if err := os.WriteFile(readyPath, []byte("ready"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "ready:", err)
		return 1
	}
	time.Sleep(60 * time.Second)
	return 0
}

// runlockHoldEmptyFixture is the "runlock-hold-empty" helper mode. It holds the flock
// on an empty daemon.lock, as the holder in the 5fd08069 measurement did: a process
// that is not a daemon. It lets go and exits 0 when the file args[2] appears.
func runlockHoldEmptyFixture(args []string) int {
	lockPath, readyPath, letGoPath := args[0], args[1], args[2]
	fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		return 1
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(os.Stderr, "flock:", err)
		return 1
	}
	if err := os.WriteFile(readyPath, []byte("ready"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "ready:", err)
		return 1
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(letGoPath); err == nil {
			return 0
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0
}

// waitForExit polls for the exit of pid until grace elapses. It returns true once the
// process is gone. The tests read the end of their own lock-holder child with it.
func waitForExit(pid int, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// writeRecordFile stages an owner record into path via the production writer, so the
// eviction tests read exactly the on-disk shape lockRunFolder writes.
func writeRecordFile(t *testing.T, path string, rec ownerRecord) {
	t.Helper()
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatalf("open record file: %v", err)
	}
	defer func() { _ = syscall.Close(fd) }()
	writeOwnerRecord(fd, rec)
}

// spawnLockHolder starts the runlock-hold fixture on lockPath and waits for it to be
// ready (flock taken, record written). termMode is "" or "term-ignore"; nodeOverride
// forces a foreign machine identity when non-empty.
func spawnLockHolder(t *testing.T, lockPath, termMode, nodeOverride string) *exec.Cmd {
	t.Helper()
	cmd := startLockHolder(t, lockPath, termMode, nodeOverride)
	// Reap the holder as soon as it exits. It is a child of the test process, so
	// without this a SIGTERM/SIGKILL leaves a zombie whose pid still answers
	// Kill(pid,0). The tests read the end of the holder with that probe (waitForExit).
	// The eviction ladder itself waits on the lock, and needs no reaping.
	reaped := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(reaped) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-reaped
	})
	return cmd
}

// startLockHolder starts the runlock-hold fixture and waits for it to be ready. It does
// not reap the child. The caller does.
func startLockHolder(t *testing.T, lockPath, termMode, nodeOverride string) *exec.Cmd {
	t.Helper()
	exe, env := helperCommand(t, "runlock-hold")
	ready := lockPath + ".ready"
	cmd := exec.Command(exe, lockPath, termMode, ready, nodeOverride)
	cmd.Env = buildEnv(env)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return cmd
		}
		if time.Now().After(deadline) {
			// No cleanup owns the holder yet. End it here, so that it does not keep the lock.
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			t.Fatalf("lock holder never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startEmptyLockHolder starts the runlock-hold-empty fixture on lockPath and waits until
// it holds the lock. The returned func makes the fixture let go and waits for its exit.
func startEmptyLockHolder(t *testing.T, lockPath string) (letGo func()) {
	t.Helper()
	exe, env := helperCommand(t, "runlock-hold-empty")
	ready, goFile := lockPath+".ready", lockPath+".go"
	cmd := exec.Command(exe, lockPath, ready, goFile)
	cmd.Env = buildEnv(env)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock holder never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() {
		t.Helper()
		if err := os.WriteFile(goFile, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Fatal("lock holder did not let go")
		}
	}
}
