//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
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
// Measured on a Windows VM against f6010b97 and 89cb6289 (rows WJ01 to WJ07, and WJ08 on
// 89cb6289). The SSH server puts a session in a job that ends its processes when the
// session ends. Its limit flags are 0x2800: kill on job close, breakaway allowed. The
// serving process of the reference is outside that job and lives on after the session.
// The launcher is inside it.
//
// Not measured: how the reference leaves the job. The rows show only the membership.
// CREATE_BREAKAWAY_FROM_JOB is claustrum's own choice. A process that is in no job ignores
// the flag.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP | createBreakawayFromJob}
}

// startDetached starts the daemonized child. A job that does not allow breakaway refuses
// the start with ERROR_ACCESS_DENIED. The start then runs once more without the breakaway
// flag, so the daemon starts inside the job, as it did before claustrum set the flag.
// 89cb6289 does the same and serves from inside the job (Windows VM, row JB).
//
// An exec.Cmd refuses a second Start, so the second try runs a copy of cmd. On success
// cmd.Process names the started process, as after a plain Start.
func startDetached(cmd *exec.Cmd) error {
	err := cmd.Start()
	if err == nil || !errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createBreakawayFromJob == 0 {
		return err
	}
	// The line is the reference's, on the stderr of the launcher. Measured on a Windows
	// VM against 89cb6289 (row JB, a job with the limit flags 0x2000).
	logWarnf("[daemon] detached spawn failed (%v); retrying without breakaway", err)
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

// openHeldDaemonLog opens the existing remote-server.log for append. openDaemonLog calls it
// after the rotate and the fresh create failed. On Windows that is the case of a
// second daemon on a live socket: the first daemon holds the log open, so the rename
// fails. The second daemon then logs to the same file. Every write of both daemons goes
// to the end of the file, so no line of either one is lost.
//
// Measured on a Windows VM (rows WN04 and WJ04): 89cb6289 logs to the file there, and its
// launcher returns at once. It truncates the file, and the earlier lines of the first
// daemon are lost. claustrum does not copy that loss (DIVERGENCES.md D21). In the same
// rows the file of claustrum kept every line of both daemons. A first daemon of an
// earlier claustrum build does not write in append mode. That mix is not measured. A
// path that is not a regular file is refused.
func openHeldDaemonLog(path string) *os.File {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return nil
	}
	return f
}
