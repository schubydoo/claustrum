//go:build windows

package main

import (
	"os"
	"syscall"
)

// newSysProcAttr puts the child in a new process group (Windows has no setpgid;
// CREATE_NEW_PROCESS_GROUP is the closest analogue).
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// signalProcessGroup terminates the child. Windows has no POSIX signals / process
// groups for arbitrary kills, so this is a best-effort TerminateProcess.
func signalProcessGroup(proc *os.Process, signame string) {
	_ = proc.Kill()
}
