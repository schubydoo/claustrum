//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// ignoreSigterm is a no-op on Windows: the "ignore-term" helper mode and its
// escalation test are Unix-only (whole-tree teardown there goes through the Job
// Object, not POSIX signals), so this only exists to satisfy the shared
// helperproc reference on the Windows build.
func ignoreSigterm() {}

// stubProcIDs is empty on Windows, which has no process group id or session id
// of the POSIX kind.
func stubProcIDs() string { return "" }

// stubNewSession starts a child of the "cli-stub" mode in a new process group,
// the closest Windows analogue of a new session.
func stubNewSession(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// exitZeroOnSigterm is a no-op on Windows for the same reason: the "term-exit0"
// helper mode and its exit-line test are Unix-only.
func exitZeroOnSigterm() {}

// execOnSigterm is a no-op on Windows: the "term-exec" helper mode and its reap test
// are Linux-only.
func execOnSigterm(string) {}

// exitZeroOnSigtermAfter is a no-op on Windows: the "term-exit0-gate" helper mode and
// its reap tests are Linux-only.
func exitZeroOnSigtermAfter(string) {}
