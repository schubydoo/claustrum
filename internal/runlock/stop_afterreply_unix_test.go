//go:build unix

package runlock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The lock step of -stop after its shutdown request, as on 5fd08069 (measured on
// Linux and macOS). The holder of the lock is a descriptor of the test process
// itself, and every signal goes through the signalHolder seam, so no test here sends
// a real signal.

// holdLockOf takes the flock on daemon.lock of dir through its own open of the file.
// A nil rec leaves the file empty. The returned func lets go of the lock once.
func holdLockOf(t *testing.T, dir string, rec *OwnerRecord) func() {
	t.Helper()
	fd, err := syscall.Open(filepath.Join(dir, LockName), syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	if rec != nil {
		writeOwnerRecord(fd, *rec)
	}
	var once sync.Once
	letGo := func() { once.Do(func() { _ = syscall.Close(fd) }) }
	t.Cleanup(letGo)
	return letGo
}

// signalLog records the signals that -stop sends through the seam, with their times.
type signalLog struct {
	mu   sync.Mutex
	sigs []syscall.Signal
	at   []time.Time
}

func (l *signalLog) add(sig syscall.Signal) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sigs = append(l.sigs, sig)
	l.at = append(l.at, time.Now())
}

func (l *signalLog) list() ([]syscall.Signal, []time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]syscall.Signal(nil), l.sigs...), append([]time.Time(nil), l.at...)
}

// seamStopSignals replaces signalHolder for the test. onSignal runs for each signal,
// and the seam reports the signal as delivered.
func seamStopSignals(t *testing.T, onSignal func(syscall.Signal)) *signalLog {
	t.Helper()
	old := signalHolder
	log := &signalLog{}
	signalHolder = func(_ int, sig syscall.Signal) bool {
		log.add(sig)
		onSignal(sig)
		return true
	}
	t.Cleanup(func() { signalHolder = old })
	return log
}

func setStopLockWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := stopLockWait
	stopLockWait = d
	t.Cleanup(func() { stopLockWait = old })
}

func lockText(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, LockName))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func stopRecordText() string {
	return fmt.Sprintf(`{"pid":%d,"role":"stop","node":%q}`+"\n", os.Getpid(), nodeID())
}

// A lock that comes free during the tries gives "stopped" and no signal. -stop writes
// nothing into daemon.lock, so the record of the holder stays (measured).
func TestStopAfterReplyTakesTheLockThatComesFree(t *testing.T) {
	requireNodeID(t)
	setStopLockWait(t, 10*time.Second)
	dir := shortTempDir(t)
	letGo := holdLockOf(t, dir, &OwnerRecord{Pid: 1 << 30, Role: "serve", Node: nodeID()})
	holderText := lockText(t, dir)
	sigs := seamStopSignals(t, func(syscall.Signal) {})
	const after = 150 * time.Millisecond
	start := time.Now()
	timer := time.AfterFunc(after, letGo)
	t.Cleanup(func() { timer.Stop() })

	word, release := StopAfterReply(filepath.Join(dir, "rpc.sock"))
	took := time.Since(start)
	if word != StopWordStopped {
		t.Errorf("word = %q, want %q", word, StopWordStopped)
	}
	if took < after {
		t.Errorf("-stop returned after %s, before the holder let go at %s", took, after)
	}
	if got, _ := sigs.list(); len(got) != 0 {
		t.Errorf("-stop sent %v to a holder that let go", got)
	}
	if got := lockText(t, dir); got != holderText {
		t.Errorf("daemon.lock before release = %q, want the record of the holder %q", got, holderText)
	}
	release()
	if got := lockText(t, dir); got != holderText {
		t.Errorf("daemon.lock after release = %q, want the record of the holder %q", got, holderText)
	}
}

// A daemon of this socket that still holds the lock after the tries gets SIGTERM.
// The word is "terminated", and -stop removes the socket and daemon.token (measured).
// Only after the signal does -stop write its own record, and release empties the file
// (measured on Linux, and on macOS for "killed").
func TestStopAfterReplyTerminatesTheHolder(t *testing.T) {
	requireNodeID(t)
	setStopLockWait(t, 200*time.Millisecond)
	seamServeHolder(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	stageStaleSocket(t, sock)
	token := stageToken(t, sock)
	const pid = 1 << 30
	letGo := holdLockOf(t, dir, &OwnerRecord{Pid: pid, Role: "serve", Node: nodeID(), InstanceID: "x", StartedAt: 1})
	sigs := seamStopSignals(t, func(sig syscall.Signal) {
		if sig == syscall.SIGTERM {
			letGo()
		}
	})
	logs := captureStopLog(t)

	start := time.Now()
	word, release := StopAfterReply(sock)
	if word != StopWordTerminated {
		t.Fatalf("word = %q, want %q", word, StopWordTerminated)
	}
	got, at := sigs.list()
	if len(got) != 1 || got[0] != syscall.SIGTERM {
		t.Fatalf("signals = %v, want one SIGTERM", got)
	}
	if waited := at[0].Sub(start); waited < stopLockWait {
		t.Errorf("SIGTERM came %s after the start, before the lock wait of %s", waited, stopLockWait)
	}
	wantLinesInOrder(t, logs.String(),
		fmt.Sprintf(`[daemon] stop: run dir is held by a live daemon, pid %d (instance "x"); sending SIGTERM`, pid),
		fmt.Sprintf(`[daemon] stop: previous daemon pid %d (instance "x") exited after SIGTERM`, pid))
	assertGone(t, sock, "socket")
	assertGone(t, token, "daemon.token")
	if got, want := lockText(t, dir), stopRecordText(); got != want {
		t.Errorf("daemon.lock before release = %q, want the stop record %q", got, want)
	}
	release()
	if got := lockText(t, dir); got != "" {
		t.Errorf("daemon.lock after release = %q, want empty", got)
	}
}

// A holder that ignores SIGTERM gets SIGKILL after the SIGTERM grace, and the word
// is "killed" (measured).
func TestStopAfterReplyKillsAHolderThatIgnoresSIGTERM(t *testing.T) {
	requireNodeID(t)
	setStopLockWait(t, 100*time.Millisecond)
	oldTerm, oldPoll := stopTermGrace, runDirPollInterval
	stopTermGrace, runDirPollInterval = 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { stopTermGrace, runDirPollInterval = oldTerm, oldPoll })
	seamServeHolder(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	stageStaleSocket(t, sock)
	token := stageToken(t, sock)
	const pid = 1 << 30
	letGo := holdLockOf(t, dir, &OwnerRecord{Pid: pid, Role: "serve", Node: nodeID(), InstanceID: "5ab5", StartedAt: 1})
	sigs := seamStopSignals(t, func(sig syscall.Signal) {
		if sig == syscall.SIGKILL {
			letGo()
		}
	})
	logs := captureStopLog(t)

	word, release := StopAfterReply(sock)
	t.Cleanup(release)
	if word != StopWordKilled {
		t.Fatalf("word = %q, want %q", word, StopWordKilled)
	}
	got, at := sigs.list()
	if len(got) != 2 || got[0] != syscall.SIGTERM || got[1] != syscall.SIGKILL {
		t.Fatalf("signals = %v, want SIGTERM then SIGKILL", got)
	}
	if gap := at[1].Sub(at[0]); gap < stopTermGrace {
		t.Errorf("SIGKILL came %s after SIGTERM, before the grace of %s", gap, stopTermGrace)
	}
	want := fmt.Sprintf(`[daemon] stop: WARNING previous daemon pid %d (instance "5ab5") ignored SIGTERM for %s; sending SIGKILL (its Claude Code children, if any, are ended next, before this daemon serves)`+"\n", pid, stopTermGrace)
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log misses %q\n--- got ---\n%s", want, logs.String())
	}
	assertGone(t, sock, "socket")
	assertGone(t, token, "daemon.token")
}

// A holder whose lock file has no record gets no signal. The word is "survivor", and
// -stop removes nothing (measured).
func TestStopAfterReplyLeavesAHolderThatIsNoDaemon(t *testing.T) {
	setStopLockWait(t, 100*time.Millisecond)
	setStopForeignWait(t, 50*time.Millisecond)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	stageStaleSocket(t, sock)
	token := stageToken(t, sock)
	holdLockOf(t, dir, nil)
	sigs := seamStopSignals(t, func(syscall.Signal) {})
	logs := captureStopLog(t)

	word, release := StopAfterReply(sock)
	release()
	if word != StopWordSurvivor {
		t.Errorf("word = %q, want %q", word, StopWordSurvivor)
	}
	if got, _ := sigs.list(); len(got) != 0 {
		t.Errorf("-stop sent %v to a holder that is no daemon", got)
	}
	want := fmt.Sprintf(`[daemon] stop: WARNING %s is held by a live process we will not signal (its holder is not a daemon (role "")); leaving it alone`+"\n", filepath.Join(dir, LockName))
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log misses %q\n--- got ---\n%s", want, logs.String())
	}
	assertKept(t, sock, "socket")
	assertKept(t, token, "daemon.token")
	if got := lockText(t, dir); got != "" {
		t.Errorf("daemon.lock = %q, want the empty file of the holder", got)
	}
}

// With no daemon.lock, -stop prints "stopped" at once and makes no file
// (claustrum's own, not measured).
func TestStopAfterReplyWithNoLockFile(t *testing.T) {
	dir := shortTempDir(t)
	word, release := StopAfterReply(filepath.Join(dir, "rpc.sock"))
	release()
	if word != StopWordStopped {
		t.Errorf("word = %q, want %q", word, StopWordStopped)
	}
	assertGone(t, filepath.Join(dir, LockName), "a daemon.lock that -stop made")
}

// The shipped wait is 3 s (measured), and 0.5 s more for a holder that is no daemon.
// The tests shrink both, so this pins the values themselves.
func TestStopLockWaitIsThreeSeconds(t *testing.T) {
	if stopLockWait != 3*time.Second {
		t.Errorf("stopLockWait = %s, want 3s", stopLockWait)
	}
	if stopForeignWait != 500*time.Millisecond {
		t.Errorf("stopForeignWait = %s, want 500ms", stopForeignWait)
	}
}

func setStopForeignWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := stopForeignWait
	stopForeignWait = d
	t.Cleanup(func() { stopForeignWait = old })
}

// A holder that is no daemon gets stopForeignWait more of lock tries after the lock
// wait, before "survivor" (measured on Linux and macOS).
func TestStopAfterReplyTriesLongerForAHolderThatIsNoDaemon(t *testing.T) {
	setStopLockWait(t, 100*time.Millisecond)
	setStopForeignWait(t, 300*time.Millisecond)
	dir := shortTempDir(t)
	holdLockOf(t, dir, nil)
	sigs := seamStopSignals(t, func(syscall.Signal) {})

	start := time.Now()
	word, release := StopAfterReply(filepath.Join(dir, "rpc.sock"))
	took := time.Since(start)
	release()
	if word != StopWordSurvivor {
		t.Errorf("word = %q, want %q", word, StopWordSurvivor)
	}
	if want := stopLockWait + stopForeignWait; took < want {
		t.Errorf("-stop printed %q after %s, before the tries of %s", word, took, want)
	}
	if got, _ := sigs.list(); len(got) != 0 {
		t.Errorf("-stop sent %v to a holder that is no daemon", got)
	}
}

// A lock that comes free during those more tries gives "stopped" and no line
// (claustrum's own, not measured).
func TestStopAfterReplyTakesTheLockInTheMoreTries(t *testing.T) {
	setStopLockWait(t, 100*time.Millisecond)
	setStopForeignWait(t, 5*time.Second)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	stageStaleSocket(t, sock)
	letGo := holdLockOf(t, dir, nil)
	sigs := seamStopSignals(t, func(syscall.Signal) {})
	logs := captureStopLog(t)
	const after = 300 * time.Millisecond
	timer := time.AfterFunc(after, letGo)
	t.Cleanup(func() { timer.Stop() })

	word, release := StopAfterReply(sock)
	release()
	if word != StopWordStopped {
		t.Errorf("word = %q, want %q", word, StopWordStopped)
	}
	if got, _ := sigs.list(); len(got) != 0 {
		t.Errorf("-stop sent %v to a holder that let go", got)
	}
	if got := logs.String(); strings.Contains(got, "will not signal") {
		t.Errorf("log has the survivor line for a lock that came free\n--- got ---\n%s", got)
	}
	assertKept(t, sock, "socket")
}

// A lock that another daemon took during the SIGTERM wait gives "survivor" and the
// line of 5fd08069 that names the new holder (measured on Linux). The new holder gets
// no signal, and the socket and daemon.token stay.
func TestStopAfterReplyLeavesTheNewHolderOfALockThatChangedHands(t *testing.T) {
	requireNodeID(t)
	setStopLockWait(t, 50*time.Millisecond)
	oldTerm, oldPoll := stopTermGrace, runDirPollInterval
	stopTermGrace, runDirPollInterval = 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { stopTermGrace, runDirPollInterval = oldTerm, oldPoll })
	oldCmd := isServeCmdline
	calls := 0
	// The old holder is our serve process at the first check only: it is gone after
	// the SIGTERM.
	isServeCmdline = func(int, string) bool { calls++; return calls == 1 }
	t.Cleanup(func() { isServeCmdline = oldCmd })
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	stageStaleSocket(t, sock)
	token := stageToken(t, sock)
	const oldPid, newPid = 1 << 30, 1<<30 + 1
	letGo := holdLockOf(t, dir, &OwnerRecord{Pid: oldPid, Role: "serve", Node: nodeID(), InstanceID: "aa", StartedAt: 1})
	sigs := seamStopSignals(t, func(sig syscall.Signal) {
		if sig == syscall.SIGTERM {
			// The old holder lets go and the new one takes the lock before -stop
			// tries it again, because the seam runs inside the signal call.
			letGo()
			holdLockOf(t, dir, &OwnerRecord{Pid: newPid, Role: "serve", Node: nodeID(), InstanceID: "bb", StartedAt: 2})
		}
	})
	logs := captureStopLog(t)

	word, release := StopAfterReply(sock)
	release()
	if word != StopWordSurvivor {
		t.Errorf("word = %q, want %q", word, StopWordSurvivor)
	}
	if got, _ := sigs.list(); len(got) != 1 || got[0] != syscall.SIGTERM {
		t.Errorf("signals = %v, want the one SIGTERM to the old holder", got)
	}
	want := fmt.Sprintf(`WARN  [daemon] stop: %s changed hands during eviction (now pid %d (instance "bb")); leaving the new holder alone`+"\n", filepath.Join(dir, LockName), newPid)
	if got := logs.String(); !strings.Contains(got, want) {
		t.Errorf("log misses %q\n--- got ---\n%s", want, got)
	}
	assertKept(t, sock, "socket")
	assertKept(t, token, "daemon.token")
}
