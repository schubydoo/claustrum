//go:build linux

package sysproc

import (
	"os"

	"golang.org/x/sys/unix"
)

// AwaitExit blocks until the child pid has exited. It reaps nothing
// (WNOWAIT), so the pid and its group stay reserved until the wait of
// exec.Cmd. A stop that ends between the two never sends its group kill after
// the reap. An error other than EINTR ends the step, and the step then reports
// false: it did not see the exit.
func AwaitExit(pid int) bool {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err == nil
		}
	}
}

// SurelyRuns reports that the child p has not exited. A child that exited and
// is not yet reaped does not run. The test waits with WNOWAIT, so it reaps
// nothing. A test that fails counts as a child that does not run.
func SurelyRuns(p *os.Process) bool {
	var info unix.Siginfo
	err := unix.Waitid(unix.P_PID, p.Pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
	return err == nil && info.Signo != int32(unix.SIGCHLD)
}
