//go:build linux

package git

import "golang.org/x/sys/unix"

// awaitLeaderExit blocks until the read-tree process pid has exited. It reaps
// nothing (WNOWAIT), so the pid and its group stay reserved until the wait of
// exec.Cmd. The watch of lfsroom.go ends between the two, so its group kill
// never comes after the reap. An error other than EINTR ends the step.
func awaitLeaderExit(pid int) {
	var info unix.Siginfo
	for {
		if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil); err != unix.EINTR {
			return
		}
	}
}
