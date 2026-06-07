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

// signalProcessGroup delivers a signal to the child's entire process group
// (negative pid). Best-effort.
func signalProcessGroup(proc *os.Process, signame string) {
	_ = syscall.Kill(-proc.Pid, parseSignal(signame))
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
