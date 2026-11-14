package supervisor

import (
	"errors"

	"golang.org/x/sys/unix"
)

// exitSeenUnreaped waits until the child pid has exited, and leaves it unreaped
// (WNOWAIT). It reports whether the wait saw the exit.
func exitSeenUnreaped(pid int) bool {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err == nil
	}
}

// groupMadeAtSpawn reports whether the process group of pid has the id pid, as the
// group that the spawn made for the child (Setpgid).
func groupMadeAtSpawn(pid int) bool {
	g, err := unix.Getpgid(pid)
	return err == nil && g == pid
}
