//go:build windows

package main

import (
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// newSysProcAttr puts the child in a new process group (Windows has no setpgid;
// CREATE_NEW_PROCESS_GROUP is the closest analogue). A kill ends the direct child only
// (see procGroup).
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// reapProcessGroup is the Windows counterpart of the Unix kill(-pgid) that
// git.worktree_create uses to reap a checkout descendant that outlived git and held
// the output pipe. Windows has no POSIX process-group signal, and this git exec path
// runs in no Job Object, so there is nothing to tear down here beyond what
// cmd.WaitDelay already did (it closed the daemon's own pipe ends, so the reply is
// bounded). The measured pipe-holding-descendant scenario is a POSIX smudge/hook
// orphan and is not reproduced on Windows; a stray descendant is left to exit on its
// own rather than reaped. Nil-safe no-op so the shared caller stays cross-platform.
func reapProcessGroup(proc *os.Process) {}

// procGroup is the kill handle of one spawned child on Windows: a process handle with
// the right to terminate. There is no Job Object. A kill ends the direct child only, with
// exit code 1. The descendants of the child live on.
//
// Measured on a Windows VM against 89cb6289: a spawned child is in no job (stub line
// "job=false jobflags=none"). process.kill in every signal form, process.killAndWait in
// every form, -stop and server.shutdown end only the direct child, with exit code 1. The
// grandchild, the great-grandchild and an orphaned grandchild are alive 1 s, 5 s and 15 s
// later. A later -stop does not end them. A hard kill of the daemon ends no child.
// claustrum gave the same states beside 89cb6289 in a later run on that VM.
//
// The handle stays open until the exit drain of the child is over (close). A kill in
// that window meets a process that has ended, and TerminateProcess answers "Access is
// denied.". 89cb6289 logged that text for the escalation of process.killAndWait when
// kids of the child held its stdout and stderr.
type procGroup struct {
	mu sync.Mutex
	h  windows.Handle // 0 once closed, or after a failed open
}

// confineProcess opens the kill handle of the just-started child. It always returns a
// non-nil *procGroup: on a failure the group has a zero handle, and signal() falls back
// to proc.Kill, rather than failing the spawn. os.Process keeps its own handle
// privately, so the process is opened again by pid. That pid cannot be reused while
// os.Process holds its handle.
func confineProcess(proc *os.Process) (*procGroup, error) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(proc.Pid))
	if err != nil {
		return &procGroup{}, err
	}
	return &procGroup{h: h}, nil
}

// signal ends the direct child. Windows has no POSIX signals, so signame is ignored:
// every signal form ends the child the same way (measured, see procGroup). It returns
// the error of TerminateProcess. With no handle it falls back to proc.Kill and returns
// nil. Nil-receiver safe.
func (g *procGroup) signal(proc *os.Process, _ string) error {
	if g != nil {
		g.mu.Lock()
		if g.h != 0 {
			err := windows.TerminateProcess(g.h, 1)
			g.mu.Unlock()
			return os.NewSyscallError("TerminateProcess", err)
		}
		g.mu.Unlock()
	}
	_ = proc.Kill()
	return nil
}

// close releases the kill handle. It ends nothing. Nil-receiver safe and idempotent.
func (g *procGroup) close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.h != 0 {
		_ = windows.CloseHandle(g.h)
		g.h = 0
	}
}
