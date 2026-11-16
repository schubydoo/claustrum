//go:build linux

package sysproc

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// AwaitExitStatus blocks until the child pid has exited. It reaps nothing
// (WNOWAIT), so the pid and its group stay reserved until the wait of
// exec.Cmd. A stop that ends between the two never sends its group kill after
// the reap. An error other than EINTR ends the step, and the step then reports
// false: it did not see the exit. clean reports an exit with status 0, from the
// si_status of the wait. After a signal si_status holds the signal number, which
// is never 0.
func AwaitExitStatus(pid int) (seen, clean bool) {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err == nil {
			return true, siginfoStatus(&info) == 0
		}
		if err != unix.EINTR {
			return false, false
		}
	}
}

// siginfoStatus is the si_status of the siginfo of a child. The union of the
// siginfo starts after three int32 fields, at the alignment of a pointer.
// si_pid and si_uid come first in it, and si_status follows them.
func siginfoStatus(info *unix.Siginfo) int32 {
	const ptr = unsafe.Sizeof(uintptr(0))
	const union = (12 + ptr - 1) / ptr * ptr
	return *(*int32)(unsafe.Add(unsafe.Pointer(info), union+8))
}

// SurelyRuns reports that the child p has not exited. A child that exited and
// is not yet reaped does not run. The test waits with WNOWAIT, so it reaps
// nothing. A test that fails counts as a child that does not run.
func SurelyRuns(p *os.Process) bool {
	var info unix.Siginfo
	err := unix.Waitid(unix.P_PID, p.Pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
	return err == nil && info.Signo != int32(unix.SIGCHLD)
}
