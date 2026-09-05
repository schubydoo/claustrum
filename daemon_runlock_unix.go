//go:build unix

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Run-dir lock, matching the reference daemon (upstream 4534d86). A -serve daemon
// takes an exclusive flock on <dir(socket)>/daemon.lock for its whole lifetime and
// writes an owner record into it, so only one serve daemon owns a socket directory at
// a time. When a prior LIVE sibling serve daemon still holds the lock, the new daemon
// evicts it (SIGTERM, then SIGKILL) before taking over — this is what makes a restart
// deterministically replace its predecessor rather than coexisting with it until the
// idle timeout (the 7d193f89 behavior).
//
// The lock lives in the socket's own directory, so it contends only with a daemon
// started against that same directory. On the honest path the live lock-holder wrote
// its own pid into the record, so the pid the ladder signals is that holder. The
// signal guards below (holderSignalRefusal) are defense in depth for the off-path
// cases: a stale record left by a crash, a foreign process that flocked the file, or a
// recycled pid.
//
// This file is the shared core for both unix targets. The machine identity (nodeID)
// and the holder-verification reads are OS-split: Linux uses /proc
// (daemon_runlock_linux.go), macOS uses sysctl (daemon_runlock_darwin.go). Windows
// ships no run-dir lock at all (daemon_runlock_windows.go).
const runDirLockName = "daemon.lock"

// Eviction timing, matching the reference (4534d86, measured): after SIGTERM the new
// daemon polls for the holder's exit for up to runDirTermGrace, then escalates to
// SIGKILL and polls for up to runDirKillGrace, checking every runDirPollInterval. They
// are package vars so a test can shrink them.
var (
	runDirTermGrace    = 2 * time.Second
	runDirKillGrace    = 1 * time.Second
	runDirPollInterval = 50 * time.Millisecond
)

// isServeCmdline reports whether pid is one of our own -serve daemons bound to socket.
// Its real implementation is OS-specific (realIsServeCmdline: /proc on Linux,
// KERN_PROCARGS2 on macOS). A package var so the eviction test can drive the
// SIGTERM/SIGKILL ladder against a real helper child without forging that helper's
// argv into a "-serve -socket=..." shape.
var isServeCmdline = realIsServeCmdline

// ownerRecord is the JSON the daemon writes into daemon.lock. The field order and the
// omitempty set reproduce the reference's record so a successor daemon reading it sees
// the same shape. Pid has no omitempty (a 0 pid is still emitted); role is "serve" for
// a serve daemon; node is omitted when the machine identity is unknown.
type ownerRecord struct {
	Pid        int    `json:"pid"`
	Role       string `json:"role,omitempty"`
	Node       string `json:"node,omitempty"`
	InstanceID string `json:"instanceId,omitempty"`
	StartedAt  int64  `json:"startedAt,omitempty"`
}

// claimRunDir takes the run-dir lock and writes this daemon's owner record, evicting a
// prior live sibling serve daemon if one holds it. It runs BEFORE the socket is bound,
// matching the reference order (run-dir claim -> socket bind -> token persist).
//
// Claiming is best-effort: every failure logs a warning and returns, and the daemon
// serves without run-dir ownership — it never aborts startup. The returned release func
// truncates the record and drops the flock on graceful shutdown; the file itself is left
// in place (not unlinked), also matching the reference. When the lock could not be taken
// the release func is a no-op.
func claimRunDir(socket, role string) func() {
	dir := filepath.Dir(socket)
	path := filepath.Join(dir, runDirLockName)
	noop := func() {}

	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		logWarnf("[daemon] run dir: cannot open %s (%v); serving without run-dir ownership", path, err)
		return noop
	}
	if !lockRunDir(fd, path, socket) {
		_ = syscall.Close(fd)
		return noop
	}
	writeOwnerRecord(fd, ownerRecord{
		Pid:        os.Getpid(),
		Role:       role,
		Node:       nodeID(),
		InstanceID: newRunDirInstanceID(),
		StartedAt:  time.Now().UnixMilli(),
	})
	return func() {
		_ = syscall.Ftruncate(fd, 0)
		_ = syscall.Close(fd)
	}
}

// lockRunDir attempts the non-blocking exclusive flock. On contention it tries to evict
// the live holder and retries once; if the lock still cannot be taken it gives up (the
// caller serves without ownership).
func lockRunDir(fd int, path, socket string) bool {
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		return true
	} else if err != syscall.EWOULDBLOCK {
		logWarnf("[daemon] run dir: flock %s failed (%v); serving without run-dir ownership", runDirLockName, err)
		return false
	}
	if !evictRunDirHolder(path, socket) {
		return false
	}
	// The holder is gone, so the lock should now be free. Retry once — if a different
	// process grabbed it in the gap, leave that new holder alone.
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		logWarnf("[daemon] run dir: %s changed hands during eviction; serving without run-dir ownership", runDirLockName)
		return false
	}
	return true
}

// evictRunDirHolder reads the current owner record and, when it names a live sibling
// serve daemon that is safe to signal, runs the SIGTERM->SIGKILL ladder. It returns
// true when the holder is gone (evicted, or already exited), false when the holder must
// be left alone or survived the ladder.
func evictRunDirHolder(path, socket string) bool {
	holder, err := readOwnerRecord(path)
	if err != nil || holder.Pid <= 1 {
		logWarnf("[daemon] run dir: %s is locked but its owner record is unusable; serving without run-dir ownership", runDirLockName)
		return false
	}
	if reason := holderSignalRefusal(holder, socket); reason != "" {
		logWarnf("[daemon] run dir: not signaling pid %d, the holder of %s (%s); serving without run-dir ownership", holder.Pid, runDirLockName, reason)
		return false
	}
	logInfof("[daemon] run dir: %s is held by a live daemon (pid %d); sending SIGTERM", runDirLockName, holder.Pid)
	if !signalHolder(holder.Pid, syscall.SIGTERM) {
		return holderGone(holder.Pid)
	}
	if waitForExit(holder.Pid, runDirTermGrace) {
		logInfof("[daemon] run dir: previous daemon pid %d exited after SIGTERM", holder.Pid)
		return true
	}
	logWarnf("[daemon] run dir: previous daemon pid %d ignored SIGTERM; sending SIGKILL", holder.Pid)
	if !signalHolder(holder.Pid, syscall.SIGKILL) {
		return holderGone(holder.Pid)
	}
	if waitForExit(holder.Pid, runDirKillGrace) {
		logInfof("[daemon] run dir: previous daemon pid %d exited after SIGKILL", holder.Pid)
		return true
	}
	logWarnf("[daemon] run dir: previous daemon pid %d survived SIGKILL; serving without run-dir ownership", holder.Pid)
	return false
}

// signalHolder sends sig to pid. It returns true when the signal was delivered, false
// when it could not be (already gone, or not permitted) — in which case the caller
// resolves the outcome with holderGone.
func signalHolder(pid int, sig syscall.Signal) bool {
	err := syscall.Kill(pid, sig)
	if err == nil {
		return true
	}
	if err == syscall.ESRCH {
		return false // already gone
	}
	logWarnf("[daemon] run dir: signal %d to pid %d failed (%v); serving without run-dir ownership", sig, pid, err)
	return false
}

// holderGone reports whether pid is no longer present (a bare existence probe). Used to
// distinguish "the holder already exited" (evict succeeded) from "we may not signal it".
func holderGone(pid int) bool { return syscall.Kill(pid, 0) == syscall.ESRCH }

// waitForExit polls for pid's exit until grace elapses, checking every
// runDirPollInterval. It returns true once the process is gone.
func waitForExit(pid int, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(runDirPollInterval)
	}
}

// holderSignalRefusal returns a reason to REFUSE signalling the lock holder, or "" when
// it is safe to signal. The guards: never signal a non-serve holder, an unusable pid,
// our own pid, a holder on another machine, a holder in another pid namespace (Linux),
// or a pid whose command line is not our serve command for this socket.
//
// The last two guards read OS-specifically (pidNamespaceRefusal, isServeCmdline). On
// Linux both use /proc. On macOS there is no /proc: pidNamespaceRefusal is a no-op (no
// pid namespaces) and isServeCmdline uses KERN_PROCARGS2. The reference's own macOS
// build does not verify the holder and signals the recorded pid unverified (observed on
// a macOS VM); claustrum instead verifies via KERN_PROCARGS2, so it refuses to signal a
// pid that is not our serve process. That is an intentional hardening divergence — see
// docs/DIVERGENCES.md D15.
func holderSignalRefusal(holder ownerRecord, socket string) string {
	if holder.Role != "serve" {
		return "holder is not a serve daemon"
	}
	if holder.Pid < 2 {
		return "holder recorded no usable pid"
	}
	if holder.Pid == os.Getpid() {
		return "record names our own pid"
	}
	self := nodeID()
	if holder.Node == "" || self == "" {
		return "the machine identity of the holder or of this process is unknown"
	}
	if holder.Node != self {
		return "holder runs on another machine or container that shares this directory"
	}
	if reason := pidNamespaceRefusal(holder.Pid); reason != "" {
		return reason
	}
	if !isServeCmdline(holder.Pid, socket) {
		return "holder is not our serve process for this socket"
	}
	return ""
}

// matchesServeArgv reports whether argv is one of our own daemons: argv0 basename ==
// self, a -serve/--serve flag, and a -socket/--socket naming exactly socket (as either
// "=value" or a separate following value). Pure, so the flag-parsing is unit-testable
// without a live process entry, and shared by the Linux and macOS holder checks.
func matchesServeArgv(argv []string, socket, self string) bool {
	if len(argv) == 0 || self == "" || filepath.Base(argv[0]) != self {
		return false
	}
	serve, sockMatch := false, false
	for i, a := range argv {
		switch {
		case a == "-serve" || a == "--serve":
			serve = true
		case a == "-socket="+socket || a == "--socket="+socket:
			sockMatch = true
		case (a == "-socket" || a == "--socket") && i+1 < len(argv) && argv[i+1] == socket:
			sockMatch = true
		}
	}
	return serve && sockMatch
}

// selfBase is our own executable's basename, used to recognise a sibling daemon. On
// failure it returns "", which makes matchesServeArgv refuse (no argv0 can equal "") so
// an unidentifiable self never signals a holder.
func selfBase() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Base(exe)
}

// newRunDirInstanceID returns a 32-hex-character random id for the owner record, the
// same 16-random-bytes shape the reference uses. It returns "" on the (unreachable)
// crypto/rand failure, and the field is then omitted.
//
// Standalone note: this is generated here so the run-dir lock is a self-contained PR.
// When the capabilities instanceId (a separate slice) also lands, unify the two so the
// on-disk record carries the same instanceId the daemon reports on the wire.
func newRunDirInstanceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// writeOwnerRecord marshals rec and writes it, followed by a newline, at offset 0 of the
// already-locked fd, truncating first so a shorter record never leaves stale trailing
// bytes. Best-effort: a marshal or write failure leaves the lock held but the record
// empty, and the daemon still serves.
func writeOwnerRecord(fd int, rec ownerRecord) {
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	data = append(data, '\n')
	if err := syscall.Ftruncate(fd, 0); err != nil {
		return
	}
	_, _ = syscall.Pwrite(fd, data, 0)
}

// readOwnerRecord reads and parses the owner record from the lock file.
func readOwnerRecord(path string) (ownerRecord, error) {
	var rec ownerRecord
	b, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	err = json.Unmarshal(b, &rec)
	return rec, err
}
