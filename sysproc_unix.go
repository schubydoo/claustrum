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

// signalProcessGroup delivers a signal to the child's entire process group
// (negative pid). Best-effort.
func signalProcessGroup(proc *os.Process, signame string) {
	_ = syscall.Kill(-proc.Pid, parseSignal(signame))
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
