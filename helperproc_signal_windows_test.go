//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ignoreSigterm is a no-op on Windows. The "ignore-term" helper mode and its escalation
// test are Unix-only: Windows has no POSIX signals, and a kill there ends the direct
// child only. This function only exists to satisfy the shared helperproc reference on
// the Windows build.
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

// breakawayStart asks for a start outside the job of this process
// (CREATE_BREAKAWAY_FROM_JOB), for the "breakaway-start" helper mode.
func breakawayStart(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createBreakawayFromJob}
}

// childJobState tells whether the child p is in the job of its group: "in-job" or
// "no-job". The "spawn-hold" helper writes it for a test that cannot see that job.
func childJobState(p *managedProc) string {
	if p.group == nil || p.group.job == 0 {
		return "no-job"
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p.pid))
	if err != nil {
		return "no-job"
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var res int32
	if r, _, _ := procIsProcessInJob.Call(uintptr(h), uintptr(p.group.job), uintptr(unsafe.Pointer(&res))); r == 0 || res == 0 {
		return "no-job"
	}
	return "in-job"
}
