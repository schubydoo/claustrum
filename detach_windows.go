//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/supervisor"
)

// DETACHED_PROCESS detaches the child from the parent's console; combined with a
// new process group this is the Windows analogue of setsid daemonization.
const detachedProcess = 0x00000008

// createBreakawayFromJob is CREATE_BREAKAWAY_FROM_JOB: the new process does not join
// the job of its parent. The syscall package holds no constant for it.
const createBreakawayFromJob = 0x01000000

// detachSysProcAttr starts the daemonized child detached, in a new process group and
// outside the job of the launcher.
//
// Measured on Windows against f6010b97 and 89cb6289. The SSH
// server puts a session in a job that ends its processes when the session ends. Its
// limit flags are 0x2800: kill on job close, breakaway allowed. The serving process of
// the reference is outside that job and lives on after the session. The launcher is
// inside it.
//
// Not measured: how the reference leaves the job. The rows show only the membership.
// CREATE_BREAKAWAY_FROM_JOB is claustrum's own choice. A process that is in no job ignores
// the flag.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP | createBreakawayFromJob}
}

// daemonStartEnv is the environment of the launcher without CLAUDE_SSH_CHILD and
// CLAUDE_SSH_RUN_DIR. 87375cc7 and 13047a51 keep neither name of the launcher in the
// environment of the daemon (measured on Windows). The spawn removes both names too
// (supervisor.WrapCmdWithTrampoline).
func daemonStartEnv(env []string) []string { return supervisor.DropMarkerEnv(env) }

// startDetached starts the daemonized child. A job that does not allow breakaway refuses
// the start with ERROR_ACCESS_DENIED. The start then runs once more without the breakaway
// flag, so the daemon starts inside the job, as it did before claustrum set the flag.
// 89cb6289 does the same and serves from inside the job (measured on Windows).
//
// An exec.Cmd refuses a second Start, so the second try runs a copy of cmd. On success
// cmd.Process names the started process, as after a plain Start.
func startDetached(cmd *exec.Cmd) error {
	err := cmd.Start()
	if err == nil || !errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createBreakawayFromJob == 0 {
		return err
	}
	// The line is the reference's, on the stderr of the launcher. Measured on Windows
	// against 89cb6289 (a job with the limit flags 0x2000).
	logging.Warnf("[daemon] detached spawn failed (%v); retrying without breakaway", err)
	attr := *cmd.SysProcAttr
	attr.CreationFlags &^= createBreakawayFromJob
	retry := &exec.Cmd{
		Path: cmd.Path, Args: cmd.Args, Env: cmd.Env, Dir: cmd.Dir,
		Stdin: cmd.Stdin, Stdout: cmd.Stdout, Stderr: cmd.Stderr,
		ExtraFiles: cmd.ExtraFiles, SysProcAttr: &attr,
	}
	if err := retry.Start(); err != nil {
		return err
	}
	cmd.Process = retry.Process
	return nil
}

// socketDirFailsStart is false on Windows: a failed create of the socket folder surfaces
// as the error of the log open after it (from the code, not measured).
const socketDirFailsStart = false

// daemonLogAppend is 0 on Windows: each daemon writes at the offset of its own handle.
// 5fd08069 does so (measured on Windows). A first daemon that writes on after a second
// one truncated the file leaves a block of NUL bytes, as there.
const daemonLogAppend = 0

// openDaemonLogAt opens the log at path. The prior log is renamed to remote-server.log.old
// first, and an error of that rename is ignored, as on 4534d86 and 5fd08069. The rename
// acts on a link itself, and the link target keeps its bytes (measured on Windows on
// 87375cc7 and 13047a51). Then an exclusive create makes a fresh log. The rename moves a
// folder onto a regular .old, as on 5fd08069 (measured on Windows).
//
// If the create fails, the entry at the path is judged by Lstat. Nothing at the path
// fails the start with the error of the create. A folder fails it with "is a directory".
// A regular file goes to openHeldDaemonLog, and any other kind fails with "permission
// denied".
func openDaemonLogAt(path string) (*os.File, error) {
	_ = os.Rename(path, path+".old") // rotate prior log to .old (parity)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL|daemonLogAppend, 0o600)
	if err == nil {
		return f, nil
	}
	createErr := err
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, createErr
	}
	switch {
	case fi.Mode().Type() == os.ModeDir:
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EISDIR}
	case !fi.Mode().IsRegular() && fi.Mode().Type() != os.ModeSymlink:
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EACCES}
	}
	return openHeldDaemonLog(path, fi)
}

// openHeldDaemonLog opens the existing remote-server.log for write and truncates it.
// openDaemonLogAt calls it after the rotate and the fresh create failed. On Windows that is
// the case of a second daemon on a live socket, or of another process that holds the log
// open with no delete share: the rename fails. 5fd08069 truncates the file in both cases,
// and the earlier lines are lost. .old stays as it was (measured on Windows). A link
// fails with "permission denied", because this code has no owner test. 5fd08069 writes
// through it (D8, Windows). An open that fails keeps the stdio of the launcher, as
// before.
func openHeldDaemonLog(path string, fi os.FileInfo) (*os.File, error) {
	if !fi.Mode().IsRegular() {
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EACCES}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return nil, nil
	}
	return f, nil
}
