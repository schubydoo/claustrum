//go:build unix

package main

import (
	"errors"
	"os"
	"strings"
	"syscall"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// statSpawnCommand is the stat of missingCommandError: one plain stat of the command
// path.
func statSpawnCommand(path string) error {
	_, err := os.Stat(path)
	return err
}

// procGroup is the per-process kill handle. On Unix the child already lives in
// its own process group (Setpgid, sysproc.NewSysProcAttr), so there is no extra OS state to
// hold. The type exists only so the cross-platform caller can treat every OS
// uniformly. On Windows it wraps a process handle of the direct child and its job.
type procGroup struct{}

// spawnSysProcAttr is sysproc.NewSysProcAttr for process.spawn. Unix has no jobs.
func spawnSysProcAttr(childJobs) *syscall.SysProcAttr { return sysproc.NewSysProcAttr() }

// confineProcess is a no-op on Unix — the process group was established by
// sysproc.NewSysProcAttr at spawn. It returns a non-nil handle for caller uniformity.
func confineProcess(*os.Process, childJobs) (*procGroup, error) { return &procGroup{}, nil }

// resumeChild has nothing to do on Unix: no child starts suspended.
func resumeChild(*os.Process, *syscall.SysProcAttr) error { return nil }

// childJobsAtStart gives jobsOff and logs no line: Linux and macOS have no jobs. The
// log of 52d82f0a has no job line on Linux (measured).
func childJobsAtStart(bool, childJobs) childJobs { return jobsOff }

// signal delivers the signal as signalProcessGroup does. Nil-receiver safe (it
// doesn't touch the receiver). A failed delivery returns a *signalFailed.
// 52d82f0a answers such a failure of process.kill with its text, for TERM and for
// KILL (measured on Linux and macOS, a child that runs as root). A process that is
// done, or a pid or group that is gone (ESRCH), returns nil, as before (claustrum's
// own, not measured).
func (*procGroup) signal(proc *os.Process, signame string) error {
	sig := parseSignal(signame)
	err := signalProcessGroup(proc, signame)
	if err == nil || errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return &signalFailed{name: sig.String(), err: err}
}

// endRest has nothing to end on Unix: the process group stays as it is after the
// exit of the child.
func (*procGroup) endRest() {}

// close has nothing to release on Unix.
func (*procGroup) close() {}

// signalProcessGroup delivers signame the way the reference does: SIGKILL goes
// to the child's entire process group (negative pid), every other signal goes to
// the direct child alone.
//
// The split is measured, not inferred. Spawning `sh -c "sleep 40 & ...; sleep 40"`
// and signalling the managed process at 5db5e4a:
//
//	process.kill TERM                     grandchild SURVIVES
//	process.kill KILL                     grandchild dies
//	process.killAndWait TERM escalate:false  grandchild SURVIVES
//	process.killAndWait (escalates to KILL)  grandchild dies
//
// claustrum killed the grandchild in all four cases, because it always used the
// negative pid.
//
// This matters to a client: a graceful `process.kill` is meant to ask one process
// to stop, and taking down every background job that process started is a
// different operation. SIGKILL keeps the group form deliberately — that is the
// whole-tree teardown, and it is what the reference does too.
//
// The signal to the one process goes through its process handle
// (sysproc.SignalOne). On Linux that is pidfd_send_signal, on the descriptor that
// the start of the child gave. Measured on Linux with strace against 89cb6289: the SIGTERM of process.kill,
// of process.killAndWait and of the supersede is pidfd_send_signal(<fd>, SIGTERM,
// NULL, 0), and a SIGKILL is kill(-<pid>, SIGKILL). macOS has no such call, and the
// handle sends kill(pid, sig) there.
//
// 52d82f0a keeps the split (measured on Linux and macOS): TERM, INT and HUP reach
// the leader only, and KILL reaches its group but not a process in its own session.
// It returns the error of the call. A pid below 2 gets the error of
// sysproc.RefuseSmallPid and no signal (claustrum's own).
func signalProcessGroup(proc *os.Process, signame string) error {
	if err := sysproc.RefuseSmallPid(proc.Pid); err != nil {
		return err
	}
	sig := parseSignal(signame)
	if sig == syscall.SIGKILL {
		return sysproc.KillChildGroup(proc.Pid)
	}
	return sysproc.SignalOne(proc, sig)
}

// parseSignal maps a signal NAME to a signal, reproducing the reference exactly.
// Both properties below are measured, by trapping each signal in the child and
// printing which one arrived — the reply to process.kill is {"success":true}
// whatever is sent, so it cannot distinguish them.
//
//	                 reference   claustrum (before)
//	INT / HUP / TERM   as named    as named
//	SIGINT / SIGHUP    as named    as named
//	int / hup / Int    TERM        INT / HUP / INT     <- case-sensitivity
//	QUIT / quit        TERM        QUIT                <- an extra mapping
//	bogus / 2          TERM        TERM
//
// So the match is CASE-SENSITIVE (no ToUpper) and there is NO QUIT case. Both
// mattered: "quit" produced SIGQUIT here and SIGTERM upstream, and SIGQUIT dumps
// core by default — a visibly different outcome for the same request.
func parseSignal(name string) syscall.Signal {
	switch strings.TrimPrefix(name, "SIG") {
	case "KILL":
		return syscall.SIGKILL
	case "INT":
		return syscall.SIGINT
	case "HUP":
		return syscall.SIGHUP
	case "", "TERM":
		return syscall.SIGTERM
	default:
		return syscall.SIGTERM
	}
}

// shutdownAsksEnd is true: the shutdown sweep sends SIGTERM to a child that
// argsAskEnd accepts, and the group SIGKILL later (b3081b51, measured on Linux and
// macOS).
const shutdownAsksEnd = true

// logsUnreapedKill is true: a killAndWait whose SIGKILL did not reap the child in
// killReapGrace logs one line (52d82f0a, measured on Linux and macOS).
const logsUnreapedKill = true

// killEndsDrainGroup is true: a process.kill with "signal":"KILL" inside the exit
// drain kills the group at once (b3081b51, measured on Linux and macOS).
const killEndsDrainGroup = true
