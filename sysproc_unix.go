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

func parseSignal(name string) syscall.Signal {
	switch strings.ToUpper(strings.TrimPrefix(name, "SIG")) {
	case "KILL":
		return syscall.SIGKILL
	case "INT":
		return syscall.SIGINT
	case "HUP":
		return syscall.SIGHUP
	case "QUIT":
		return syscall.SIGQUIT
	case "", "TERM":
		return syscall.SIGTERM
	default:
		return syscall.SIGTERM
	}
}
