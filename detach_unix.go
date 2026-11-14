//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/schubydoo/claustrum/internal/supervisor"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// detachSysProcAttr starts the daemonized child in a new session (reparented to
// init), detaching it from the controlling terminal.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// daemonStartEnv is the environment of the launcher without CLAUDE_SSH_CHILD and
// CLAUDE_SSH_RUN_DIR. It is the base of the environment of the daemonized child.
// Measured on Linux against c9125eb8 and 4c111e86: the daemon keeps neither name of the
// launcher. On macOS this is claustrum's own, not measured. A process.spawn child still
// gets both names, because the spawn adds them (runDirEnv).
func daemonStartEnv(env []string) []string {
	return sysproc.RemoveEnvKey(sysproc.RemoveEnvKey(env, supervisor.EnvChildMarker), supervisor.EnvRunDir)
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
// the mode kept, as on 5fd08069 (measured on Linux and macOS). A file of another user
// fails with "permission denied" and stays unchanged (D8).
//
// A link must belong to the user of the daemon, and it is opened through, as on
// 5fd08069 (measured on Linux). Its target must be a regular file of that user with
// one link (claustrum's own, not measured). Another link fails with "permission
// denied" (D8).
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
