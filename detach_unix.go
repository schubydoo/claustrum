//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"syscall"

	"github.com/schubydoo/claustrum/internal/logging"
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
// launcher. Measured on macOS too (87375cc7 and 13047a51). The place of the entries is
// not measured there. A process.spawn child still
// gets both names, because the spawn adds them (runDirEnv).
func daemonStartEnv(env []string) []string {
	return sysproc.RemoveEnvKey(sysproc.RemoveEnvKey(env, supervisor.EnvChildMarker), supervisor.EnvRunDir)
}

// startDetached starts the daemonized child. On unix it is cmd.Start. Windows has its own.
func startDetached(cmd *exec.Cmd) error { return cmd.Start() }

// socketDirFailsStart is true on unix: a failed create of the socket folder ends the
// launcher before the log open (daemonizeWithToken). Windows has its own.
const socketDirFailsStart = true

// daemonLogAppend is the append flag of the log open. On unix every write goes to the end
// of the file. Windows has its own.
const daemonLogAppend = os.O_APPEND

// daemonLogTries is the count of remove and create pairs of openDaemonLogAt. A create
// that finds a new entry at the path tries again. The retry and its count are not
// measured.
const daemonLogTries = 3

// afterLogRemove runs between the remove and the create of each try. It is a seam, so a
// test can plant an entry at the path there.
var afterLogRemove = func(string) {}

// openDaemonLogAt opens the log at path. It never opens or writes through the old entry.
// 13047a51 gives the same outcomes (measured on Linux and macOS):
//   - keepOldLog renames a regular file with one link to remote-server.log.old.
//   - One plain remove by name then deletes whatever is at the path: a link, a FIFO, a
//     socket, an empty folder or a file with a second name. A link target and the other
//     name keep their bytes. The remove is never a tree delete.
//   - An exclusive create makes a fresh regular log, mode 0600.
//
// A failed remove fails the start with its error, for example "directory not empty"
// for a folder with entries, or "operation not permitted" for the file of another user
// in a sticky folder on Linux. The entry stays.
func openDaemonLogAt(path string) (*os.File, error) {
	keepOldLog(path)
	var err error
	for range daemonLogTries {
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return nil, rerr
		}
		afterLogRemove(path)
		var f *os.File
		f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL|daemonLogAppend, 0o600)
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
	return nil, err
}

// keepOldLog judges the entry at path by Lstat and renames a regular file with one link
// to path.old. Any other entry stays for the remove of openDaemonLogAt, and a line on
// the stderr of the launcher tells so. A failed rename logs its error in the same line.
// The texts are those of 13047a51 (measured on Linux and macOS). A missing entry and a
// failed Lstat log nothing.
func keepOldLog(path string) {
	fi, err := os.Lstat(path)
	if err != nil {
		return
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.Mode().IsRegular() || st.Nlink != 1 {
		logging.Infof("[daemon] the last run's log is not kept as .old: it is no regular file, or has a second name")
		return
	}
	if err := os.Rename(path, path+".old"); err != nil {
		logging.Infof("[daemon] the last run's log is not kept as .old: %v", err)
	}
}
