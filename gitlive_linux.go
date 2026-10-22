//go:build linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// gitStillRuns reports that the git process p has not exited. A git that
// exited and is not yet reaped does not run. The test waits with WNOWAIT, so
// it reaps nothing. A test that fails counts as a git that runs (claustrum's
// own, not measured).
func gitStillRuns(p *os.Process) bool {
	var info unix.Siginfo
	err := unix.Waitid(unix.P_PID, p.Pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
	return err != nil || info.Signo != int32(unix.SIGCHLD)
}
