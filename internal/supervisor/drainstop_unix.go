//go:build linux || darwin

package supervisor

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// leaderKeptOwnGroup waits for the exit of the leader, leaves it unreaped, and reports
// whether it still led the group that the spawn made.
func leaderKeptOwnGroup(pid int) bool {
	return exitSeenUnreaped(pid) && groupMadeAtSpawn(pid)
}

// drainGroupRefused is groupNumberRefused for this daemon.
func drainGroupRefused(pgid int) bool {
	return groupNumberRefused(pgid, os.Getpid(), syscall.Getpgrp())
}

// pipesStillHeld reports whether another process still holds the write end of a pipe.
// A read end with no writer left answers POLLHUP, also with unread data in the pipe. A
// closed read end counts as not held. The same poll serves Linux and macOS.
func pipesStillHeld(pipes [2]*os.File) bool {
	for _, f := range pipes {
		if f != nil && pipeStillHeld(f) {
			return true
		}
	}
	return false
}

// pollPipe is the poll of pipeStillHeld. A seam for the tests.
var pollPipe = unix.Poll

// pollPipeTries bounds the polls of one test when each answers EINTR (claustrum's own).
const pollPipeTries = 8

// pollPipeRetry polls with no wait and polls again after EINTR, at most pollPipeTries
// times. It returns any other error at once, and EINTR after the last try.
func pollPipeRetry(fds []unix.PollFd) (int, error) {
	var n int
	var err error
	for range pollPipeTries {
		n, err = pollPipe(fds, 0)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	return n, err
}

// pipeStillHeld polls one read end with no wait. A failed poll counts as no writer, so
// it sends no signal.
func pipeStillHeld(f *os.File) bool {
	raw, err := f.SyscallConn()
	if err != nil {
		return false
	}
	held := false
	if err := raw.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := pollPipeRetry(fds)
		// No event at all is an empty pipe with a writer.
		held = err == nil && (n == 0 || fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) == 0)
	}); err != nil {
		return false
	}
	return held
}
