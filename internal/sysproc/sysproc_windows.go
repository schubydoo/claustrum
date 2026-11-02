//go:build windows

package sysproc

import (
	"os"
	"syscall"
)

// NewSysProcAttr puts the child in a new process group (Windows has no setpgid;
// CREATE_NEW_PROCESS_GROUP is the closest analogue). With no job a kill ends the direct
// child only (see procGroup in package main).
func NewSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// ReapProcessGroup is the Windows counterpart of the Unix kill(-pgid) that
// git.worktree_create uses to reap a checkout descendant that outlived git and held
// the output pipe. Windows has no POSIX process-group signal, and this git exec path
// runs in no Job Object, so there is nothing to tear down here beyond what
// cmd.WaitDelay already did (it closed the daemon's own pipe ends, so the reply is
// bounded). The measured pipe-holding-descendant scenario is a POSIX smudge/hook
// orphan and is not reproduced on Windows; a stray descendant is left to exit on its
// own rather than reaped. Nil-safe no-op so the shared caller stays cross-platform.
func ReapProcessGroup(proc *os.Process) {}
