//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// Eviction timing: after SIGTERM the new daemon tries the lock again every
// runDirPollInterval for up to runDirTermGrace. Then it sends SIGKILL and tries the lock
// for up to runDirKillGrace. The 2 s SIGTERM grace is measured against 4534d86. The 50 ms
// interval is measured on Linux with strace against 89cb6289 (row H7): 43 flock calls
// that answer EAGAIN from the SIGTERM to the lock 2.07 s later. The 1 s after SIGKILL is
// claustrum's own, not probe-measured. They are package vars so a test can shrink them.
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

// signalHolder, holderGone, and waitHolderLock are package vars over their real syscall
// implementations, the same seam idiom as isServeCmdline above. They let the eviction
// test drive the SIGKILL escalation arms — the holder already gone at SIGKILL, an
// undeliverable SIGKILL, and a holder that survives SIGKILL — none of which is
// reproducible against a live same-user process on a modern kernel (nothing survives
// SIGKILL).
var (
	signalHolder   = realSignalHolder
	holderGone     = realHolderGone
	waitHolderLock = waitLockFree
)

// holdHolder opens the hold on the holder of the lock for the serve eviction. The
// eviction opens it before it reads the identity of the holder, and sends each signal
// through it. The real hold is the one of the host cleaner (hcHoldDaemon). A package
// var, so a test drives the ladder with no real signal.
var holdHolder = hcHoldDaemon

// ownerRecord is the JSON the daemon writes into daemon.lock. The field order reproduces
// the reference's record so a successor daemon reading it sees the same shape. Pid
// has no omitempty (a 0 pid is still emitted). Role is "serve" for a serve daemon.
// Node is omitted when the machine identity is unknown.
type ownerRecord struct {
	Pid        int    `json:"pid"`
	Role       string `json:"role,omitempty"`
	Node       string `json:"node,omitempty"`
	InstanceID string `json:"instanceId,omitempty"`
	StartedAt  int64  `json:"startedAt,omitempty"`
}

// runDirClaim is what claimRunDir gives back to the daemon.
type runDirClaim struct {
	// release truncates the owner record and drops the flock on graceful shutdown. The
	// file itself stays in place (not unlinked).
	release func()
	// complete writes the full owner record: the short record plus instanceId and
	// startedAt. The daemon calls it after the reap of its start and the bind.
	complete func(instanceID string, startedAt int64)
}

// claimRunDir takes the run-dir lock and writes the short owner record of this daemon:
// pid, role and node only. It evicts a prior live sibling serve daemon if one holds the
// lock. It runs BEFORE the reap of the start and before the socket is bound.
//
// The record gets instanceId and startedAt later, through complete. Measured on Linux
// and macOS against 89cb6289 (rows D1, D2 and XL): while the new daemon waits for the
// recorded children of a dead daemon to end, daemon.lock holds
// {"pid":<n>,"role":"serve","node":"<node>"}. After that wait the same record has
// "instanceId" and "startedAt" too.
//
// Claiming is best-effort: every failure logs a warning and returns, and the daemon
// serves without run-dir ownership. It never aborts startup. When the daemon does not
// get the lock, release and complete are no-ops.
//
// A daemon that does not get the lock prints the "not the run dir's lock holder" line last.
// The reference printed it on a Linux VM (row ST02, f6010b97 and 89cb6289). There a process
// with no daemon record held the lock, and two other lines came first. claustrum prints it
// on every arm that ends without the lock. The other arms are not measured. The line says "recording
// none". claustrum still records and reaps children in that state. What the reference does
// there is not measured.
func claimRunDir(socket, role string) runDirClaim {
	dir := filepath.Dir(socket)
	path := filepath.Join(dir, runDirLockName)
	// notHolder is the return of every arm that ends without the lock.
	notHolder := func() runDirClaim {
		logInfof("%s", runDirNotHolderLine)
		return runDirClaim{release: func() {}, complete: func(string, int64) {}}
	}

	// O_NOFOLLOW: never follow a pre-existing daemon.lock symlink. If the socket dir is
	// shared-writable, another local user could plant a symlink to a daemon-writable file
	// and have startup truncate it; O_NOFOLLOW makes the open fail (ELOOP) instead. Since
	// claimRunDir only ever creates daemon.lock as a regular file, the honest path is
	// unaffected. This matches the reference's observable behavior: measured on linux, the
	// reference refuses a planted daemon.lock symlink and leaves the target untouched
	// (scratch/probe/runlock-linux-refbehavior-4534d86.md M1), where pre-fix claustrum
	// followed the link and truncated the target. The flag is set on both unix targets;
	// the macOS reference's symlink behavior is unmeasured, so on macOS this is
	// off-wire defense-in-depth rather than confirmed parity.
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		logWarnf("[daemon] serve: cannot open %s (%v); proceeding without run-dir ownership", path, err)
		return notHolder()
	}
	if !lockRunDir(fd, path, socket) {
		_ = syscall.Close(fd)
		return notHolder()
	}
	short := ownerRecord{Pid: os.Getpid(), Role: role, Node: nodeID()}
	writeOwnerRecord(fd, short)
	return runDirClaim{
		release: func() {
			_ = syscall.Ftruncate(fd, 0)
			_ = syscall.Close(fd)
		},
		complete: func(instanceID string, startedAt int64) {
			full := short
			full.InstanceID, full.StartedAt = instanceID, startedAt
			// No truncate here. The full record is the short record plus two members, so
			// it is never shorter. One write at offset 0 replaces the short record, and
			// the file is never empty between the two. An empty file reads as role "" to
			// a second daemon, and that daemon then serves without the lock.
			if data, err := json.Marshal(full); err == nil {
				_, _ = syscall.Pwrite(fd, append(data, '\n'), 0)
			}
		},
	}
}

// lockRunDir attempts the non-blocking exclusive flock. On contention it tries to evict
// the live holder. The eviction waits on the lock, so it usually returns with the lock
// taken. If the holder is gone and the eviction did not take the lock, lockRunDir retries
// once. If the lock still cannot be taken it gives up (the caller serves without
// ownership).
func lockRunDir(fd int, path, socket string) bool {
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		return true
	} else if err != syscall.EWOULDBLOCK {
		// The reference logs one line for ANY non-EWOULDBLOCK flock error and calls it
		// "unsupported" with the errno text (measured 4534d86: EOPNOTSUPP/ENOLCK/EINVAL/
		// EACCES all render the same line; scratch/probe/runlock-log-4534d86.md).
		logWarnf("[daemon] serve: flock %s unsupported (%v); proceeding without run-dir ownership", path, err)
		return false
	}
	gone, locked := evictRunDirHolder(fd, path, socket)
	if !gone {
		return false
	}
	if locked {
		return true
	}
	// The holder is gone, so the lock should now be free. Retry once — if a different
	// process grabbed it in the gap, leave that new holder alone.
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// This line's wording is claustrum's own. The reference's changed-hands line was
		// not captured at runtime.
		logWarnf("[daemon] serve: %s changed hands during eviction; proceeding without run-dir ownership", path)
		return false
	}
	return true
}

// evictRunDirHolder reads the current owner record and, when it names a live sibling
// serve daemon that is safe to signal, runs the SIGTERM->SIGKILL ladder. gone is true
// when the holder is gone (evicted, or already exited), false when the holder must be
// left alone or survived the ladder. locked is true when the wait of the ladder took
// the lock on fd.
//
// The ladder waits for the holder by trying the lock again, as 89cb6289 does. Measured
// on Linux with strace (rows H7, P5, P6, E3, N3): after the SIGTERM 89cb6289 repeats
// flock(LOCK_EX|LOCK_NB) on daemon.lock every 50 ms and sends no kill(pid, 0). The lock
// frees when the holder ends, also while the parent of the holder has not collected it.
// kill(pid, 0) still answers for such a holder. Build ac5cadb of claustrum used that
// probe. In row N3 (gap 0.05 s, 3 of 3 runs) it read a killed holder as alive and served
// without the lock.
func evictRunDirHolder(fd int, path, socket string) (gone, locked bool) {
	// The reference logs a "previous owner of <rundir>: <outcome>" summary, with
	// outcome terminated (SIGTERM), killed
	// (SIGKILL), or survivor (left in place). The three outcomes and the eviction
	// wording are measured against 4534d86 (scratch/probe/runlock-log-4534d86.md).
	// Only the daemon log MESSAGE text is matched here; claustrum keeps its own level
	// tag and banner (an accepted format divergence). The outcome on the rare
	// pid-reused / signal-failed edges is extrapolated from the captured pattern
	// (removed -> terminated/killed, left -> survivor).
	rundir := filepath.Dir(socket)
	summary := func(outcome string) {
		logInfof("[daemon] serve: previous owner of %s: %s", rundir, outcome)
	}
	holder, err := readOwnerRecord(path)
	// A holder whose record names no daemon role is left alone. That covers a lock file
	// with no record at all (role ""). The two lines are the reference's, measured on a
	// Linux VM against f6010b97 and 89cb6289 (row ST02). In that row a helper holds the
	// lock and the file is empty. A record with another role text is not measured.
	if holder.Role != "serve" && holder.Role != "stop" {
		logWarnf("[daemon] serve: WARNING %s is held by a live process we will not signal (its holder is not a daemon (role %q)); leaving it alone", path, holder.Role)
		summary("survivor")
		return false, false
	}
	if err != nil || holder.Pid <= 1 {
		logWarnf("[daemon] serve: %s is locked but its owner record is unusable; proceeding without run-dir ownership", path)
		return false, false
	}
	// A --stop that still holds the lock is left in place (measured 4534d86).
	if holder.Role == "stop" {
		logWarnf("[daemon] serve: %s is held by a --stop (pid %d) that has not let go; leaving it", path, holder.Pid)
		summary("survivor")
		return false, false
	}
	// Generic holder-signal refusal (holder on another machine, our own pid, etc.).
	// This line's wording is claustrum's own. The reference's line was not captured at
	// runtime. The summary outcome (survivor) is the captured held-by-stop pattern.
	//
	// The order is: the tests of the record, then the open of the hold, then the reads
	// of the process, then the signal through the hold. Measured on Linux with strace
	// against 89cb6289 (row P6, 2 of 2 runs): pidfd_open(<pid>, 0), then the read of
	// /proc/<pid>/ns/pid, then the read of /proc/<pid>/cmdline, then
	// pidfd_send_signal(<fd>, SIGTERM, NULL, 0). From the code: the descriptor names
	// the process that had the pid at the open, so a process that takes the pid later
	// gets no signal. On macOS the hold is kill(pid, sig), and the order has no effect.
	refuse := func(reason string) {
		logWarnf("[daemon] serve: not signaling pid %d, the holder of %s (%s); proceeding without run-dir ownership", holder.Pid, path, reason)
		summary("survivor")
	}
	if reason := holderRecordRefusal(holder); reason != "" {
		refuse(reason)
		return false, false
	}
	send, release := holdHolder(holder.Pid)
	defer release()
	if reason := holderProcessRefusal(holder, socket); reason != "" {
		refuse(reason)
		return false, false
	}
	who := holderText(holder)
	logInfof("[daemon] serve: run dir is held by a live daemon, %s; sending SIGTERM", who)
	if !deliverToHolder(send, holder.Pid, syscall.SIGTERM) {
		if holderGone(holder.Pid) {
			summary("terminated")
			return true, false
		}
		// SIGTERM could not be delivered but the holder is still present (a non-ESRCH
		// error): we proceed without ownership, so it is left in place — summary
		// "survivor", per the documented outcome mapping above.
		summary("survivor")
		return false, false
	}
	if waitHolderLock(fd, runDirTermGrace) {
		// Rows P5 (Linux and macOS, 5 of 5 runs each) and N5: 89cb6289 prints this line
		// when the lock comes within the grace.
		logInfof("[daemon] serve: previous daemon %s exited after SIGTERM", who)
		summary("terminated")
		return true, true
	}
	// The lock is still held after the SIGTERM grace. Re-verify that the pid is STILL our
	// serve holder before escalating: if the original exited and its pid was reused by an
	// unrelated same-user process during the grace, a signal by pid would end that
	// innocent process. On Linux the hold is a pidfd of the original. This matches the
	// reference, which re-verifies the lock holder immediately before its SIGKILL (measured
	// on linux — scratch/probe/runlock-linux-refbehavior-4534d86.md M2); pre-fix claustrum
	// verified only before SIGTERM and diverged. A refusal now almost always means the pid
	// was reused and the original holder is gone; on the rare chance the holder is still
	// alive but a holder-inspection read failed transiently, the retry flock simply fails
	// and we serve without ownership — never an innocent SIGKILL.
	if reason := holderSignalRefusal(holder, socket); reason != "" {
		logInfof("[daemon] serve: pid %d is no longer our serve holder before SIGKILL (%s); treating the previous daemon as gone", holder.Pid, reason)
		summary("terminated")
		return true, false
	}
	// The text is the one of 89cb6289 (Linux rows H7 and N3). The SIGKILL goes through
	// the hold of the SIGTERM. Rows H7 and N3 show pidfd_send_signal with the same
	// descriptor number for both signals.
	logWarnf("[daemon] serve: WARNING previous daemon %s ignored SIGTERM for %s; sending SIGKILL (its Claude Code children, if any, are ended next, before this daemon serves)", who, runDirTermGrace)
	if !deliverToHolder(send, holder.Pid, syscall.SIGKILL) {
		if holderGone(holder.Pid) {
			summary("killed")
			return true, false
		}
		// SIGKILL could not be delivered and the holder is still present: left in place,
		// summary "survivor" (same mapping as the SIGTERM arm above).
		summary("survivor")
		return false, false
	}
	if waitHolderLock(fd, runDirKillGrace) {
		// The reference emits no "exited after SIGKILL" line — it goes straight to the
		// summary (measured 4534d86, and 89cb6289 in rows H7 and N3).
		summary("killed")
		return true, true
	}
	// Not runtime-reproducible on a modern kernel (nothing survives SIGKILL); the exact
	// reference wording for this line was not capturable, so claustrum's own message
	// stands with the normalized prefix/tail. See scratch/probe/runlock-log-4534d86.md.
	logWarnf("[daemon] serve: previous daemon pid %d survived SIGKILL; proceeding without run-dir ownership", holder.Pid)
	summary("survivor")
	return false, false
}

// stopTermGrace is how long -stop waits for the lock holder to let go of daemon.lock
// after SIGTERM, before it sends SIGKILL. 4 s, measured against f6010b97 and 90fca6e6
// on a Linux VM. The serve eviction above uses its own runDirTermGrace. A var so a
// test can shrink it.
var stopTermGrace = 4 * time.Second

// stopKillGrace is how long -stop polls daemon.lock after SIGKILL. 1.5 s, measured
// against f6010b97 and 90fca6e6 on a Linux VM (1.506 to 1.527 s). A var so a test
// can shrink it.
var stopKillGrace = 1500 * time.Millisecond

// stopRunDirHolder is the -stop fallback after the connect to the socket failed. It
// returns the stdout word. Measured against f6010b97 and 90fca6e6 on a Linux VM:
//
//   - If daemon.lock is missing or free, the word is "none".
//   - If a live serve daemon of this binary for this socket holds it, -stop sends
//     SIGTERM and polls the lock. The word is "terminated" when the lock frees.
//   - If the lock is still held after stopTermGrace, -stop sends SIGKILL and polls
//     the lock for stopKillGrace. The word is "killed" when the lock frees.
//   - If the lock is still held after stopKillGrace, the word is "survivor". -stop
//     does not wait longer for the lock.
//   - If the holder is not that daemon, -stop does not signal it. The word is
//     "survivor".
//
// claustrum's own rule: the word is also "survivor" when the holder check fails
// again before SIGKILL.
//
// The holder check is holderSignalRefusal, the same check the serve eviction uses.
// The exact rule the Linux reference applies was not isolated. On macOS the
// reference signals any live holder whose record says role serve on this node.
// It does not check the command line. claustrum keeps its holder check on macOS,
// so a foreign holder there gets "survivor". This is part of D15 (measured on a
// macOS VM against f6010b97 and 90fca6e6).
//
// Five stderr lines copy the reference text: the SIGTERM line, the exit line, the
// SIGKILL line, the "will not signal" line and the "survived SIGKILL" line. The
// line about the second holder check is claustrum's own. claustrum keeps its own
// level tag, as the serve eviction lines do.
//
// -stop opens daemon.lock without O_CREAT, so it never creates the file. It never
// writes an owner record into it. The references also never create or remove it,
// and write no record into it.
//
// The returned release func closes the lock fd. The caller removes the socket and
// daemon.token first and calls release after, so a free lock stays held across
// the removals. A new -serve takes the lock before it binds, so it cannot bind a
// socket that -stop then removes. Measured with strace on a Linux VM, the
// reference takes the lock and removes both files before it writes its word.
func stopRunDirHolder(socket string) (string, func()) {
	word, fd := stopRunDirHolderFD(socket)
	if fd < 0 {
		return word, func() {}
	}
	return word, func() { _ = syscall.Close(fd) }
}

// stopRunDirHolderFD does the work of stopRunDirHolder. It returns the open lock
// fd, or -1 when daemon.lock could not be opened.
func stopRunDirHolderFD(socket string) (string, int) {
	path := filepath.Join(filepath.Dir(socket), runDirLockName)
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return stopWordNone, -1
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != syscall.EWOULDBLOCK {
		// Free (this process now holds it until the deferred close), or flock is
		// not supported here. No live daemon can be named either way.
		return stopWordNone, fd
	}
	holder, err := readOwnerRecord(path)
	if err != nil || holderSignalRefusal(holder, socket) != "" {
		logWarnf("[daemon] stop: WARNING %s is held by a live process we will not signal (pid %d is not a %s --serve process for %s); leaving it alone", path, holder.Pid, selfBase(), socket)
		return stopWordSurvivor, fd
	}
	// The two lines below name the instance only when the record has one, as the serve
	// eviction lines do. Measured on Linux against 89cb6289 (row N5): for the short
	// record of a daemon in its start, both lines hold `pid <n>` and no instance.
	who := holderText(holder)
	logInfof("[daemon] stop: run dir is held by a live daemon, %s; sending SIGTERM", who)
	if !signalHolder(holder.Pid, syscall.SIGTERM) && !holderGone(holder.Pid) {
		// The holder is present but SIGTERM did not reach it. -stop leaves it.
		return stopWordSurvivor, fd
	}
	if waitLockFree(fd, stopTermGrace) {
		logInfof("[daemon] stop: previous daemon %s exited after SIGTERM", who)
		return stopWordTerminated, fd
	}
	// Check the holder again before SIGKILL, as the serve eviction does. If its pid
	// now names another process, a SIGKILL hits that process instead.
	if reason := holderSignalRefusal(holder, socket); reason != "" {
		logWarnf("[daemon] stop: pid %d is no longer our serve holder before SIGKILL (%s); leaving it alone", holder.Pid, reason)
		return stopWordSurvivor, fd
	}
	logWarnf("[daemon] stop: WARNING previous daemon pid %d (instance %q) ignored SIGTERM for %s; sending SIGKILL (its Claude Code children, if any, are ended next, before this daemon serves)", holder.Pid, holder.InstanceID, stopTermGrace)
	if !signalHolder(holder.Pid, syscall.SIGKILL) && !holderGone(holder.Pid) {
		return stopWordSurvivor, fd
	}
	if !waitLockFree(fd, stopKillGrace) {
		logWarnf("[daemon] stop: WARNING previous daemon pid %d (instance %q) survived SIGKILL (uninterruptible?); proceeding without run-dir ownership", holder.Pid, holder.InstanceID)
		return stopWordSurvivor, fd
	}
	return stopWordKilled, fd
}

// waitLockFree polls the flock on fd every runDirPollInterval until it is taken or
// grace elapses. It reports whether the lock was taken. The lock frees when its
// holder exits, even before a parent reaps it, so this sees an exit that
// kill(pid, 0) misses on a zombie. -stop and the serve eviction both wait with it.
func waitLockFree(fd int, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(runDirPollInterval)
	}
}

// holderText names the holder in the eviction lines: its pid, and its instance when its
// record has one. Measured on Linux against 89cb6289: a record with an instance gives
// `pid <n> (instance "<hex>")` in the SIGTERM line and in the exited line (row E3), and
// in the SIGKILL line (row H7). A record with no instance, the short record of a daemon
// that has not bound yet, gives `pid <n>` in the SIGTERM line and the exited line (rows
// P5 and N5) and in the SIGKILL line (row N3).
func holderText(holder ownerRecord) string {
	if holder.InstanceID == "" {
		return fmt.Sprintf("pid %d", holder.Pid)
	}
	return fmt.Sprintf("pid %d (instance %q)", holder.Pid, holder.InstanceID)
}

// signalHolder sends sig to pid. It returns true when the signal was delivered, false
// when it could not be (already gone, or not permitted) — in which case the caller
// resolves the outcome with holderGone. -stop uses it. The serve eviction opens its
// hold earlier (holdHolder) and sends through deliverToHolder.
//
// The signal goes through hcHoldDaemon, the hold that the host cleaner uses. On Linux
// that is pidfd_open and then pidfd_send_signal, with kill(pid, sig) on a kernel that
// has neither call. On macOS the hold is kill(pid, sig). Measured on Linux with strace
// against 89cb6289 (row N5): the SIGTERM of -stop is pidfd_send_signal. Not measured:
// the call for the SIGKILL of -stop.
func realSignalHolder(pid int, sig syscall.Signal) bool {
	send, release := hcHoldDaemon(pid)
	defer release()
	return deliverToHolder(send, pid, sig)
}

// deliverToHolder sends sig through the hold send. It reports whether the signal was
// delivered.
func deliverToHolder(send func(syscall.Signal) error, pid int, sig syscall.Signal) bool {
	err := send(sig)
	if err == nil {
		return true
	}
	if err == syscall.ESRCH {
		return false // already gone
	}
	logWarnf("[daemon] serve: signal %d to pid %d failed (%v); proceeding without run-dir ownership", sig, pid, err)
	return false
}

// holderGone reports whether pid is no longer present (a bare existence probe). Used to
// distinguish "the holder already exited" (evict succeeded) from "we may not signal it".
func realHolderGone(pid int) bool { return syscall.Kill(pid, 0) == syscall.ESRCH }

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
	if reason := holderRecordRefusal(holder); reason != "" {
		return reason
	}
	return holderProcessRefusal(holder, socket)
}

// holderRecordRefusal holds the guards of holderSignalRefusal that test the record
// only. They read nothing of the process of the holder.
func holderRecordRefusal(holder ownerRecord) string {
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
	return ""
}

// holderProcessRefusal holds the guards of holderSignalRefusal that read the process
// of the holder: its pid namespace (Linux) and its command line.
func holderProcessRefusal(holder ownerRecord, socket string) string {
	if reason := pidNamespaceRefusal(holder.Pid); reason != "" {
		return reason
	}
	if !isServeCmdline(holder.Pid, socket) {
		return "holder is not our serve process for this socket"
	}
	return ""
}

// matchesServeArgv reports whether argv is one of our own daemons: argv0 basename ==
// self, a -serve/--serve flag, and a -socket/--socket naming socket by its cleaned form
// (as either "=value" or a separate following value). Pure, so the flag-parsing is
// unit-testable without a live process entry, and shared by the Linux and macOS holder
// checks.
func matchesServeArgv(argv []string, socket, self string) bool {
	if len(argv) == 0 || self == "" || filepath.Base(argv[0]) != self {
		return false
	}
	// Compare socket paths by their cleaned form. claimRunDir derives the lock path with
	// filepath.Join (which cleans) and the kernel cleans the bind path, so "/d/./rpc.sock"
	// and "/d/rpc.sock" share one daemon.lock and one endpoint. An exact string compare
	// would fail to recognise the holder across those equivalent spellings, refuse to
	// evict it, and leave two daemons live on the same socket. This matches the reference,
	// which normalizes the socket path in its holder match (measured on linux: a successor
	// with the cleaned spelling evicts a predecessor started with "/./" —
	// scratch/probe/runlock-linux-refbehavior-4534d86.md M3); claustrum's exact compare
	// diverged.
	want := filepath.Clean(socket)
	serve, sockMatch := false, false
	for i, a := range argv {
		switch {
		case a == "-serve" || a == "--serve":
			serve = true
		case strings.HasPrefix(a, "-socket="):
			sockMatch = sockMatch || filepath.Clean(strings.TrimPrefix(a, "-socket=")) == want
		case strings.HasPrefix(a, "--socket="):
			sockMatch = sockMatch || filepath.Clean(strings.TrimPrefix(a, "--socket=")) == want
		case (a == "-socket" || a == "--socket") && i+1 < len(argv):
			sockMatch = sockMatch || filepath.Clean(argv[i+1]) == want
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
