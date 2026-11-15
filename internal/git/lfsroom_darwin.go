//go:build darwin

package git

import "golang.org/x/sys/unix"

// awaitLeaderExit blocks until the read-tree process pid has exited. macOS has
// no waitid in golang.org/x/sys/unix, so the step waits for NOTE_EXIT on a
// kqueue. That event comes at the exit and reaps nothing, so the pid and its
// group stay reserved until the wait of exec.Cmd. A registration that fails
// (ESRCH for a process that already exited and waits for its reap) ends the
// step at once. So does a kqueue that cannot be made: the watch then ends
// before the exit and sends no kill (claustrum's own, not measured).
func awaitLeaderExit(pid int) {
	kq, err := unix.Kqueue()
	if err != nil {
		return
	}
	defer func() { _ = unix.Close(kq) }()
	var ev unix.Kevent_t
	unix.SetKevent(&ev, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	ev.Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		return
	}
	out := make([]unix.Kevent_t, 1)
	for {
		if _, err := unix.Kevent(kq, nil, out, nil); err != unix.EINTR {
			return
		}
	}
}
