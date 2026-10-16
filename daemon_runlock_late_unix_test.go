//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// runlockHoldEmptyFixture is the "runlock-hold-empty" helper mode. It holds the flock
// on an empty daemon.lock, as the holder of round task005-01 did: a process that is not
// a daemon. It lets go and exits 0 when the file args[2] appears.
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

// shrinkLatePeriod sets a short period for the late tries and counts each try. Call it
// before the claim, so that its cleanup runs after the cleanup of the claim.
func shrinkLatePeriod(t *testing.T) *atomic.Int32 {
	t.Helper()
	oldPeriod, oldFlock := runDirLatePeriod, lateFlock
	var tries atomic.Int32
	runDirLatePeriod = 20 * time.Millisecond
	lateFlock = func(fd, how int) error {
		tries.Add(1)
		return syscall.Flock(fd, how)
	}
	t.Cleanup(func() { runDirLatePeriod, lateFlock = oldPeriod, oldFlock })
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

// TestLateRunDirLockAfterHolderLetsGo follows cells L1 (Linux and macOS VMs) and L4
// (Linux VM) of round task005-01 on 5fd08069. A start finds daemon.lock held by a process that is
// not a daemon and serves without the lock. After the holder lets go, a later try takes
// the lock, logs the late line once and writes the full record with the startedAt of the
// start. The daemon holds the lock until its shutdown, which leaves an empty file.
func TestLateRunDirLockAfterHolderLetsGo(t *testing.T) {
	buf := captureLogBuf(t)
	tries := shrinkLatePeriod(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	letGo := startEmptyLockHolder(t, lockPath)

	s, err := newServerOnSocket(sock, "tok", "", wireLogOptions{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.releaseRunDir(); s.procs.killAll() })
	if !strings.Contains(buf.String(), runDirNotHolderLine) {
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

	rec, err := json.Marshal(ownerRecord{Pid: os.Getpid(), Role: "serve", Node: nodeID(), InstanceID: s.instanceID, StartedAt: s.startedAt})
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
	if !s.procs.noRecords {
		t.Error("a late lock changed the decision of the start about child records")
	}

	// A good try ends the tries: no second late line (cell L6, Linux VM).
	after := tries.Load()
	if pollUntil(20*runDirLatePeriod, func() bool { return tries.Load() > after }) {
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

// TestLateRunDirLockSilentWhileHeld follows cell L3 of round task005-01 (5fd08069, Linux
// VM): a try that finds the lock held logs nothing and writes nothing.
func TestLateRunDirLockSilentWhileHeld(t *testing.T) {
	buf := captureLogBuf(t)
	tries := shrinkLatePeriod(t)
	dir := shortTempDir(t)
	lockPath := filepath.Join(dir, runDirLockName)
	startEmptyLockHolder(t, lockPath)
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	claim := claimRunDir(filepath.Join(dir, "s.sock"), "serve")
	t.Cleanup(claim.release)
	const inst = "0123456789abcdef0123456789abcdef"
	claim.complete(inst, 1790962940734)
	if !pollUntil(5*time.Second, func() bool { return tries.Load() >= 3 }) {
		t.Fatalf("%d late tries, want at least 3", tries.Load())
	}

	// Only the lines of this claim count: other tests share the capture.
	if lines := lateLines(buf, inst); len(lines) != 0 {
		t.Errorf("a failed try logged the late line %q, want no late line", lines)
	}
	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != 0 || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("daemon.lock changed while the holder held it: %d bytes, mtime %v, before %v", after.Size(), after.ModTime(), before.ModTime())
	}
	if !lockIsHeld(t, lockPath) {
		t.Error("the holder lost the lock to a failed try")
	}
}

// TestLateRunDirLockStopsAtShutdown pins claustrum's own rule: no try takes the lock
// once the shutdown began, also when the holder lets go after that.
func TestLateRunDirLockStopsAtShutdown(t *testing.T) {
	buf := captureLogBuf(t)
	tries := shrinkLatePeriod(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	letGo := startEmptyLockHolder(t, lockPath)

	s, err := newServerOnSocket(sock, "tok", "", wireLogOptions{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.releaseRunDir(); s.procs.killAll() })

	s.signalShutdown()
	letGo()
	at := tries.Load()
	if pollUntil(20*runDirLatePeriod, func() bool { return tries.Load() > at }) {
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

// TestLateRunDirLockTwoSurvivorsOneWins follows cell L6 of round task005-01 (5fd08069,
// Linux VM). Two starts serve without the lock. After the holder lets go, the first try
// wins. Only that claim logs the late line and writes its record. The other keeps
// trying and fails in silence.
func TestLateRunDirLockTwoSurvivorsOneWins(t *testing.T) {
	buf := captureLogBuf(t)
	tries := shrinkLatePeriod(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	letGo := startEmptyLockHolder(t, lockPath)

	const instA, instB = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	a := claimRunDir(sock, "serve")
	t.Cleanup(a.release)
	b := claimRunDir(sock, "serve")
	t.Cleanup(b.release)
	a.complete(instA, 1)
	b.complete(instB, 2)

	letGo()
	won := func() int { return len(lateLines(buf, instA)) + len(lateLines(buf, instB)) }
	if !pollUntil(5*time.Second, func() bool { return won() > 0 }) {
		t.Fatalf("no late line after the holder let go; log = %q", buf.String())
	}
	// The other claim keeps trying: wait for two more tries.
	at := tries.Load()
	if !pollUntil(5*time.Second, func() bool { return tries.Load() >= at+2 }) {
		t.Fatalf("the losing claim stopped its tries (%d tries, then %d)", at, tries.Load())
	}
	if n := won(); n != 1 {
		t.Fatalf("%d late lines, want 1; log = %q", n, buf.String())
	}
	winner := instA
	if len(lateLines(buf, instB)) == 1 {
		winner = instB
	}
	rec, err := readOwnerRecord(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if rec.InstanceID != winner {
		t.Errorf("daemon.lock names instance %q, want the winner %q", rec.InstanceID, winner)
	}
}

// TestLateRunDirPeriodValue pins the shipped period of the late tries, which every
// late-lock test sets for itself. 30 s is measured against 5fd08069 (round task005-01,
// cells L1 and L2 on Linux and macOS VMs, cell L3 on a Linux VM).
func TestLateRunDirPeriodValue(t *testing.T) {
	if runDirLatePeriod != 30*time.Second {
		t.Errorf("late period %v, want 30s", runDirLatePeriod)
	}
}
