//go:build unix

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The -stop lock fallback, measured against f6010b97 and 90fca6e6 on a Linux VM.
// After a failed connect, -stop reads daemon.lock:
//
//	lock missing or free                    none
//	held by our serve for this socket       SIGTERM, lock frees     terminated
//	same, still held after 4 s              SIGKILL                 killed
//	same, still held 1.5 s after SIGKILL    no more waiting         survivor
//	held by a process that is not that      no signal               survivor
//
// On the survivor path -stop removes neither the socket nor daemon.token.
//
// SAFETY: every signal in these tests goes to a lock holder the test itself
// started (spawnLockHolder). The ladder reads the pid from the lock record,
// which that child wrote. No test here reaches a real socket or a real daemon.

// captureStopLog sends the stdlib log output to a buffer for the rest of the test.
func captureStopLog(t *testing.T) *syncBuffer {
	t.Helper()
	var buf syncBuffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

// seamServeHolder makes the holder check accept the test's lock-holder child.
// Its real argv is the test binary, not "-serve -socket <sock>".
func seamServeHolder(t *testing.T) {
	t.Helper()
	old := isServeCmdline
	isServeCmdline = func(int, string) bool { return true }
	t.Cleanup(func() { isServeCmdline = old })
}

func requireNodeID(t *testing.T) {
	t.Helper()
	if nodeID() == "" {
		t.Skip("the lock ladder needs a machine identity (nodeID)")
	}
}

// stageToken writes a daemon.token beside sock and returns its path.
func stageToken(t *testing.T, sock string) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(sock), persistedTokenName)
	if err := os.WriteFile(p, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertKept(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("%s removed by -stop (Lstat err = %v), want kept", what, err)
	}
}

func assertGone(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s still present after -stop (Lstat err = %v)", what, err)
	}
}

// A free daemon.lock gives "none". -stop keeps the file, and a failed connect
// still removes the stale socket and the stale token.
func TestRunStopFreeLockPrintsNone(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	stageStaleSocket(t, sock)
	token := stageToken(t, sock)
	lockPath := filepath.Join(dir, runDirLockName)
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stopPrints(t, sock, "none\n")
	if _, err := os.Lstat(lockPath); err != nil {
		t.Errorf("daemon.lock removed by -stop (Lstat err = %v), want kept", err)
	}
	assertGone(t, sock, "stale socket")
	assertGone(t, token, "stale daemon.token")
}

// With no daemon.lock, -stop prints "none" and does not create one.
func TestRunStopMissingLockNotCreated(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	stopPrints(t, sock, "none\n")
	assertGone(t, filepath.Join(dir, runDirLockName), "a daemon.lock that -stop created")
}

// A live serve holder that exits on SIGTERM gives "terminated" and the two
// reference stderr lines. The socket is missing, as in the measured reference run.
// The grace is 10 s here, so a slow exit on a loaded
// host still reads as "terminated". The pin test keeps checking the 4 s value.
func TestRunStopTerminatesLockHolder(t *testing.T) {
	requireNodeID(t)
	oldGrace := stopTermGrace
	stopTermGrace = 10 * time.Second
	t.Cleanup(func() { stopTermGrace = oldGrace })
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	holder := spawnLockHolder(t, lockPath, "", "")
	rec, err := readOwnerRecord(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	token := stageToken(t, sock)
	seamServeHolder(t)
	logs := captureStopLog(t)

	stopPrints(t, sock, "terminated\n")

	if !waitForExit(holder.Process.Pid, 3*time.Second) {
		t.Error("the lock holder is still alive after -stop printed terminated")
	}
	got := logs.String()
	for _, want := range []string{
		fmt.Sprintf("[daemon] stop: run dir is held by a live daemon, pid %d (instance %q); sending SIGTERM\n", rec.Pid, rec.InstanceID),
		fmt.Sprintf("[daemon] stop: previous daemon pid %d (instance %q) exited after SIGTERM\n", rec.Pid, rec.InstanceID),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr missing %q\n--- got ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "SIGKILL") {
		t.Errorf("SIGKILL line on a holder that exited after SIGTERM\n%s", got)
	}
	assertGone(t, token, "daemon.token")
	if _, err := os.Lstat(lockPath); err != nil {
		t.Errorf("daemon.lock removed by -stop (Lstat err = %v), want kept", err)
	}
}

// A serve holder that ignores SIGTERM gets SIGKILL after stopTermGrace, and
// -stop prints "killed". The grace is a seam, so the test does not wait 4 s.
func TestRunStopKillsHolderThatIgnoresSIGTERM(t *testing.T) {
	requireNodeID(t)
	old, oldPoll := stopTermGrace, runDirPollInterval
	stopTermGrace, runDirPollInterval = 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { stopTermGrace, runDirPollInterval = old, oldPoll })

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	holder := spawnLockHolder(t, lockPath, "term-ignore", "")
	rec, err := readOwnerRecord(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	token := stageToken(t, sock)
	seamServeHolder(t)
	logs := captureStopLog(t)

	start := time.Now()
	stopPrints(t, sock, "killed\n")
	if el := time.Since(start); el < stopTermGrace {
		t.Errorf("-stop returned after %s, before the %s SIGTERM grace", el, stopTermGrace)
	}
	if !waitForExit(holder.Process.Pid, 3*time.Second) {
		t.Error("the lock holder is still alive after -stop printed killed")
	}
	want := fmt.Sprintf("[daemon] stop: WARNING previous daemon pid %d (instance %q) ignored SIGTERM for %s; sending SIGKILL (its Claude Code children, if any, are ended next, before this daemon serves)\n", rec.Pid, rec.InstanceID, stopTermGrace)
	if got := logs.String(); !strings.Contains(got, want) {
		t.Errorf("stderr missing %q\n--- got ---\n%s", want, got)
	}
	assertGone(t, token, "daemon.token")
}

// The shipped SIGTERM grace is 4 s (measured). The test above shrinks it, so
// this pins the value itself.
func TestStopTermGraceIsFourSeconds(t *testing.T) {
	if stopTermGrace != 4*time.Second {
		t.Errorf("stopTermGrace = %s, want 4s", stopTermGrace)
	}
}

// The shipped post-SIGKILL wait is 1.5 s (measured). The arms test below shrinks
// it, so this pins the value itself.
func TestStopKillGraceIsOnePointFiveSeconds(t *testing.T) {
	if stopKillGrace != 1500*time.Millisecond {
		t.Errorf("stopKillGrace = %s, want 1.5s", stopKillGrace)
	}
}

// A live holder that is not our serve process for this socket gets no signal.
// -stop prints "survivor" and the reference WARNING line. The holder check is
// the real one here: the child's argv is the test binary, not a serve.
//
// Without the check, the ladder signals the child this test started, and
// nothing else. The test then fails on the holder's exit.
func TestRunStopLeavesForeignHolderAlone(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	holder := spawnLockHolder(t, lockPath, "", "")
	stageStaleSocket(t, sock)
	token := stageToken(t, sock)
	logs := captureStopLog(t)

	stopPrints(t, sock, "survivor\n")

	// On the survivor path -stop unlinks nothing (measured, T17e).
	assertKept(t, sock, "socket")
	assertKept(t, token, "daemon.token")

	if waitForExit(holder.Process.Pid, 300*time.Millisecond) {
		t.Fatal("-stop signalled a lock holder that is not our serve process")
	}
	want := fmt.Sprintf("[daemon] stop: WARNING %s is held by a live process we will not signal (pid %d is not a %s --serve process for %s); leaving it alone\n",
		lockPath, holder.Process.Pid, selfBase(), sock)
	if got := logs.String(); !strings.Contains(got, want) {
		t.Errorf("stderr missing %q\n--- got ---\n%s", want, got)
	}
	// The holder still owns the lock.
	fd, err := syscall.Open(lockPath, syscall.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != syscall.EWOULDBLOCK {
		t.Errorf("lock free after survivor (flock err = %v), want still held", err)
	}
}

// The seamed arms that a live child cannot reach. Each one prints "survivor" and
// unlinks neither the socket nor daemon.token:
//   - SIGTERM or SIGKILL does not reach a holder that is still present.
//   - The holder is no longer our serve process before SIGKILL.
//   - The lock is still held stopKillGrace after SIGKILL (measured, T19). This arm
//     also checks the reference line and that the wait is stopKillGrace.
//
// The record names a pid that does not exist, and every signal goes through the
// signalHolder seam, so nothing is signalled.
func TestStopRunDirHolderUndeliverableArms(t *testing.T) {
	requireNodeID(t)
	restore := func() func() {
		oc, osig, oh, og, ok, osk, op := isServeCmdline, signalHolder, holderGone, stopTermGrace, runDirKillGrace, stopKillGrace, runDirPollInterval
		return func() {
			isServeCmdline, signalHolder, holderGone = oc, osig, oh
			stopTermGrace, runDirKillGrace, stopKillGrace, runDirPollInterval = og, ok, osk, op
		}
	}
	const instance = "x"
	cases := []struct {
		name     string
		termOK   bool
		killOK   bool
		serveCmd func(calls int) bool
		wantLog  string
	}{
		{"sigterm undeliverable", false, false, func(int) bool { return true }, ""},
		{"not our serve before SIGKILL", true, false, func(calls int) bool { return calls == 1 }, "is no longer our serve holder before SIGKILL"},
		{"sigkill undeliverable", true, false, func(int) bool { return true }, "ignored SIGTERM for"},
		{"lock still held after SIGKILL", true, true, func(int) bool { return true },
			fmt.Sprintf("WARN  [daemon] stop: WARNING previous daemon pid %d (instance %q) survived SIGKILL (uninterruptible?); proceeding without run-dir ownership\n", 1<<30, instance)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(restore())
			// runDirKillGrace is short and stopKillGrace is long, so the timing check
			// below tells the two apart.
			stopTermGrace, runDirKillGrace, stopKillGrace, runDirPollInterval = 50*time.Millisecond, 50*time.Millisecond, 400*time.Millisecond, 10*time.Millisecond
			calls := 0
			isServeCmdline = func(int, string) bool { calls++; return tc.serveCmd(calls) }
			var killedAt time.Time
			signalHolder = func(_ int, sig syscall.Signal) bool {
				if sig == syscall.SIGKILL {
					killedAt = time.Now()
					return tc.killOK
				}
				return tc.termOK
			}
			holderGone = func(int) bool { return false }

			dir := shortTempDir(t)
			sock := filepath.Join(dir, "rpc.sock")
			lockPath := filepath.Join(dir, runDirLockName)
			// The test holds the flock itself on a second open file description,
			// so -stop sees it as held for the whole run.
			fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Close(fd) })
			if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			writeOwnerRecord(fd, ownerRecord{Pid: 1 << 30, Role: "serve", Node: nodeID(), InstanceID: instance, StartedAt: 1})
			stageStaleSocket(t, sock)
			token := stageToken(t, sock)
			logs := captureStopLog(t)

			stopPrints(t, sock, "survivor\n")
			done := time.Now()

			if tc.wantLog != "" && !strings.Contains(logs.String(), tc.wantLog) {
				t.Errorf("log missing %q\n--- got ---\n%s", tc.wantLog, logs.String())
			}
			assertKept(t, sock, "socket")
			assertKept(t, token, "daemon.token")
			if tc.killOK {
				if waited := done.Sub(killedAt); waited < stopKillGrace {
					t.Errorf("-stop returned %s after SIGKILL, want at least stopKillGrace (%s)", waited, stopKillGrace)
				}
			}
		})
	}
}

// A free lock stays held by -stop until release runs, so the removals of the
// socket and daemon.token happen under the lock. A -serve that starts meanwhile
// cannot take the lock, so it cannot bind a socket that -stop then removes.
func TestStopHoldsFreeLockUntilRelease(t *testing.T) {
	dir := shortTempDir(t)
	lock := filepath.Join(dir, runDirLockName)
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	word, release := stopRunDirHolder(filepath.Join(dir, "rpc.sock"))
	if word != stopWordNone {
		t.Fatalf("word = %q, want %q for a free lock", word, stopWordNone)
	}
	other, err := syscall.Open(lock, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(other) }()
	if err := syscall.Flock(other, syscall.LOCK_EX|syscall.LOCK_NB); err != syscall.EWOULDBLOCK {
		t.Fatalf("before release another fd took the lock (err %v): -stop does not hold it across the removals", err)
	}
	release()
	if err := syscall.Flock(other, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("after release the lock is still held: %v", err)
	}
}
