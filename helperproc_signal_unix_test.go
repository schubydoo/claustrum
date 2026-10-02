//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// ignoreSigterm makes the helper child ignore SIGTERM, so process.killAndWait's
// graceful signal is a no-op and it must escalate to SIGKILL (see the
// "ignore-term" helper mode and TestKillAndWaitEscalation).
func ignoreSigterm() { signal.Ignore(syscall.SIGTERM) }

// stubProcIDs is the process group, the session and the parent of this helper
// process, for the START line of the "cli-stub" mode.
func stubProcIDs() string {
	return fmt.Sprintf("pgid=%d sid=%d ppid=%d", syscall.Getpgrp(), ownSessionID(), os.Getppid())
}

// ownSessionID is getsid(0). The syscall package has no Getsid on Linux.
func ownSessionID() int {
	sid, _, _ := syscall.RawSyscall(syscall.SYS_GETSID, 0, 0, 0)
	return int(sid)
}

// stubNewSession starts a child of the "cli-stub" mode in a new session.
func stubNewSession(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
