//go:build darwin

package sysproc

import (
	"os"

	"golang.org/x/sys/unix"
)

// AwaitExitStatus blocks until the child pid has exited. macOS has no waitid
// in golang.org/x/sys/unix, so the step waits for NOTE_EXIT on a kqueue. That
// event comes at the exit and reaps nothing, so the pid and its group stay
// reserved until the wait of exec.Cmd. A registration that fails with ESRCH (a
// process that already exited and waits for its reap) ends the step at once
// and counts as a seen exit. Any other failure, also a kqueue that cannot be
// made, ends the step and reports false: it did not see the exit (claustrum's
// own, not measured). clean reports an exit with status 0. NOTE_EXITSTATUS puts
// the wait status in the data of the event. After an ESRCH the step has no
// status, and clean is false (claustrum's own, not measured).
func AwaitExitStatus(pid int) (seen, clean bool) {
	kq, err := unix.Kqueue()
	if err != nil {
		return false, false
	}
	defer func() { _ = unix.Close(kq) }()
	var ev unix.Kevent_t
	unix.SetKevent(&ev, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	ev.Fflags = unix.NOTE_EXIT | unix.NOTE_EXITSTATUS
	if _, err := unix.Kevent(kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		return err == unix.ESRCH, false
	}
	out := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, nil, out, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return false, false
		}
		return true, n == 1 && out[0].Fflags&unix.NOTE_EXITSTATUS != 0 && out[0].Data == 0
	}
}

// darwinZombie is the state SZOMB of a process in the kinfo_proc of macOS.
const darwinZombie = 5

// SurelyRuns reports that the child p has not exited. A child that exited and
// is not yet reaped does not run. A test that fails counts as a child that
// does not run.
func SurelyRuns(p *os.Process) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", p.Pid)
	return err == nil && kp.Proc.P_stat != darwinZombie
}
