package supervisor

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// darwinZombie is the state SZOMB of a process in the kinfo_proc of macOS.
const darwinZombie = 5

// exitSeenUnreaped waits until the child pid has exited, and leaves it unreaped. A
// kqueue NOTE_EXIT event on the pid gives the exit, and only the wait of the exit
// goroutine reaps the child. macOS refuses the event for a pid that exited already:
// the zombie state in its kinfo_proc then gives the exit, looked at up to
// zombieTries times, 1 ms apart, for a child in the middle of its exit. It reports
// whether the wait saw the exit. This macOS form is claustrum's own (decision-099).
func exitSeenUnreaped(pid int) bool {
	kq, err := unix.Kqueue()
	if err != nil {
		return false
	}
	defer func() { _ = unix.Close(kq) }()
	var ev unix.Kevent_t
	unix.SetKevent(&ev, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT|unix.EV_RECEIPT)
	ev.Fflags = unix.NOTE_EXIT
	// With EV_RECEIPT the add returns at once, with its error in Data.
	receipt := make([]unix.Kevent_t, 1)
	n, err := keventRetry(kq, []unix.Kevent_t{ev}, receipt)
	if err != nil || n != 1 {
		return false
	}
	if receipt[0].Flags&unix.EV_ERROR == 0 {
		return false
	}
	if e := receipt[0].Data; e != 0 {
		return unix.Errno(e) == unix.ESRCH && becomesZombie(pid)
	}
	out := make([]unix.Kevent_t, 1)
	n, err = keventRetry(kq, nil, out)
	return err == nil && n == 1 && out[0].Fflags&unix.NOTE_EXIT != 0
}

// keventRetry is unix.Kevent with no time limit, called again after EINTR.
func keventRetry(kq int, changes, events []unix.Kevent_t) (int, error) {
	for {
		n, err := unix.Kevent(kq, changes, events, nil)
		if !errors.Is(err, unix.EINTR) {
			return n, err
		}
	}
}

// zombieTries is the count of looks of becomesZombie.
const zombieTries = 20

// becomesZombie reports whether pid shows as exited and not reaped within
// zombieTries looks.
func becomesZombie(pid int) bool {
	for i := range zombieTries {
		if isZombie(pid) {
			return true
		}
		if i < zombieTries-1 {
			time.Sleep(time.Millisecond)
		}
	}
	return false
}

// isZombie reports whether pid has exited and is not reaped yet.
func isZombie(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && kp.Proc.P_stat == darwinZombie
}

// groupMadeAtSpawn reports whether the process group of pid has the id pid, as the
// group that the spawn made for the child (Setpgid). The group id comes from the
// kinfo_proc of pid.
func groupMadeAtSpawn(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && int(kp.Eproc.Pgid) == pid
}
