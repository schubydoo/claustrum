//go:build unix

package main

import (
	"os"
	"strings"
	"syscall"
)

// newSysProcAttr puts a spawned child in its own process group so the whole
// subtree can be signalled together.
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// reapProcessGroup SIGKILLs the whole process group led by proc — kill(-pgid) —
// reaping any descendant proc left behind. It is how git.worktree_create's checkout
// tears down a smudge/hook orphan that outlived git and kept the daemon's output
// pipe open past the drain cap, matching 4534d86's observed descendant reap. The command must
// have been started with newSysProcAttr (Setpgid), so proc is its group's leader
// and its pid equals the pgid. Best-effort and nil-safe: a nil or already-reaped
// process is a no-op. (The pgid stays reserved while any group member is alive, so
// this is safe to call after proc itself has been waited on.)
func reapProcessGroup(proc *os.Process) {
	if proc == nil {
		return
	}
	_ = syscall.Kill(-proc.Pid, syscall.SIGKILL)
}

// honorKeepChildren reports the effective -keep-children setting. On POSIX it is
// honored verbatim: children spawn into their own process groups (Setpgid) and
// are reparented to init when the detached daemon exits, so simply not signalling
// them on shutdown leaves them running. (Windows overrides this — see its file.)
func honorKeepChildren(requested bool) bool { return requested }

// procGroup is the per-process kill handle. On Unix the child already lives in
// its own process group (Setpgid, above), so there is no extra OS state to
// hold; the type exists only so the cross-platform caller can treat every OS
// uniformly (on Windows it wraps a Job Object).
type procGroup struct{}

// confineProcess is a no-op on Unix — the process group was established by
// newSysProcAttr at spawn. It returns a non-nil handle for caller uniformity.
func confineProcess(*os.Process) (*procGroup, error) { return &procGroup{}, nil }

// signal delivers the signal to the child's whole process group. Nil-receiver
// safe (it doesn't touch the receiver).
func (*procGroup) signal(proc *os.Process, signame string) {
	signalProcessGroup(proc, signame)
}

// close has nothing to release on Unix.
func (*procGroup) close() {}

// signalProcessGroup delivers signame the way the reference does: SIGKILL goes
// to the child's entire process group (negative pid), every other signal goes to
// the direct child alone. Best-effort.
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
// negative pid. The reference's own function table corroborates the split: it
// carries both a signalProcess and a killProcessGroup.
//
// This matters to a client: a graceful `process.kill` is meant to ask one process
// to stop, and taking down every background job that process started is a
// different operation. SIGKILL keeps the group form deliberately — that is the
// whole-tree teardown, and it is what the reference does too.
func signalProcessGroup(proc *os.Process, signame string) {
	sig := parseSignal(signame)
	if sig == syscall.SIGKILL {
		_ = syscall.Kill(-proc.Pid, sig)
		return
	}
	_ = syscall.Kill(proc.Pid, sig)
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
