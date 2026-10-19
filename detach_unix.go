//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// detachSysProcAttr starts the daemonized child in a new session (reparented to
// init), detaching it from the controlling terminal.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// startDetached starts the daemonized child. On unix it is cmd.Start. Windows has its own.
func startDetached(cmd *exec.Cmd) error { return cmd.Start() }

// daemonLogAppend is the append flag of the log open. On unix every write goes to the end
// of the file. Windows has its own.
const daemonLogAppend = os.O_APPEND

// daemonLogOwned reports whether the user of the daemon owns the entry of fi. It is a
// seam, so a test can stage an entry of another user.
var daemonLogOwned = func(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

// openHeldDaemonLog opens the regular file or the link at the log path that the rotate
// cannot move, and truncates the file. fi is its Lstat. The lines then go there with
// the mode kept, as on 5fd08069 (cell E1 of rounds task015-03 and task015-04, Linux and
// macOS VMs). A file of another user fails with "permission denied" and stays unchanged
// (D8, cell S4 of round task015-04).
//
// A link must belong to the user of the daemon, and it is opened through, as on
// 5fd08069 (cell E5 of round task015-04, Linux VM). Its target must be a regular file
// of that user with one link (claustrum's own, not measured). Another link fails with
// "permission denied" (D8, cells S1 and S2).
//
// A file with more than one link is refused the same way, so no other name is truncated
// (not measured, claustrum's own). The open does not block on a FIFO. It then tests
// that the open file is the file that was judged. An open error fails the start with
// that error (not measured).
func openHeldDaemonLog(path string, fi os.FileInfo) (*os.File, error) {
	refused := &os.PathError{Op: "open", Path: path, Err: syscall.EACCES}
	flags := syscall.O_WRONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC | daemonLogAppend
	if fi.Mode().Type() == os.ModeSymlink {
		if !daemonLogOwned(fi) {
			return nil, refused
		}
		target, err := os.Stat(path)
		if err != nil {
			return nil, refused
		}
		fi = target
		flags &^= syscall.O_NOFOLLOW
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.Mode().IsRegular() || !daemonLogOwned(fi) || st.Nlink != 1 {
		return nil, refused
	}
	fd, err := syscall.Open(path, flags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var now syscall.Stat_t
	err = syscall.Fstat(fd, &now)
	if err == nil && (now.Dev != st.Dev || now.Ino != st.Ino || now.Mode&syscall.S_IFMT != syscall.S_IFREG || now.Nlink != 1) {
		err = syscall.EACCES
	}
	if err == nil {
		err = syscall.SetNonblock(fd, false)
	}
	if err == nil {
		err = syscall.Ftruncate(fd, 0)
	}
	if err != nil {
		_ = syscall.Close(fd)
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
