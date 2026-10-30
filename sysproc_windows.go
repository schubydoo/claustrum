//go:build windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// newSysProcAttr puts the child in a new process group (Windows has no setpgid;
// CREATE_NEW_PROCESS_GROUP is the closest analogue). With no job a kill ends the direct
// child only (see procGroup).
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// statSpawnCommand is the stat of missingCommandError: one stat of the command path
// that the start uses. It answers only for an absolute path that does not exist.
//
// Measured on a Windows VM against 89cb6289 (row SSm-path, 3 of 3 runs): a session
// spawn of the missing path <dir>\nocmd.exe answers "fork/exec <dir>\nocmd.exe: The
// system cannot find the file specified." and the prior process of the session lives
// on. The lookup does not answer for that path: a path that ends in an executable
// extension passes it with no test of the file. Build ac5cadb of claustrum ran no stat
// here and ended the prior process first.
//
// A bare name and a path with no extension answer the lookup error before this stat
// (rows SSm-bare and SSm-noext). Not measured: a relative path. It gets its extension
// only at the start, so this stat leaves it alone. Not measured: a stat error other
// than a missing file. It passes, and the start answers.
func statSpawnCommand(path string) error {
	if !filepath.IsAbs(path) {
		return nil
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
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
// the right to terminate and, with jobs on, the job of the child.
//
// With jobs on (the default), each child joins a job before it runs. So the child
// starts no process before it joins its job. A process that WMI, the Task Scheduler or
// a service starts for it is outside the job. A kill ends the whole job, with exit code
// 1. The end of the child ends the rest of its job (endRest). The job never holds the
// daemon. One job for each child is claustrum's own, not measured.
//
// Measured on a Windows VM against 52d82f0a. A process.kill of the tree `cmd /c "start
// /b ping … & ping …"` answers in 11 ms. The exit frame has exitCode 1 and killedBy
// client. No ping is left 0.5 s later. process.killAndWait of that tree answers
// {"found":true,"died":true} in 21 ms, and server.shutdown answers in 13 ms. Both pings
// are gone, and a ping outside the daemon stays. When cmd exits and leaves a started
// ping, the exit frame comes at 123 ms. No ping is left then. Build c8e2f083 left the
// pings in each of these rows.
//
// With CLAUDE_SSH_NO_JOBS=1 there is no job, and a kill ends the direct child only,
// with exit code 1. The descendants of the child live on. With the switch, 52d82f0a
// leaves them at the shutdown and at the exit of the child (measured). Build 89cb6289
// leaves them in every case, for each form of process.kill and process.killAndWait,
// for -stop and for server.shutdown. The grandchild, the great-grandchild and an
// orphaned grandchild are alive 1 s, 5 s and 15 s later. A taskkill /F of the daemon
// ends no child there.
//
// An explicit TerminateJobObject ends a job. That is a kill, process.killAndWait, and
// the kill of a shutdown or of a session supersede. It is also endRest after the exit
// of the child, and the kill of a spawn that the daemon does not keep. No job has
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE. So a daemon under taskkill /F leaves its children
// alive, as 89cb6289 does (measured). 52d82f0a is not measured there. A child that
// -keep-children kept stays in its job, and nothing ends it.
//
// Each job has JOB_OBJECT_LIMIT_BREAKAWAY_OK. So a start with CREATE_BREAKAWAY_FROM_JOB
// gets a process outside the job and does not fail with "Access is denied." That is
// claustrum's own, not measured.
//
// The handles stay open until the exit drain of the child is over (close). A kill in
// that window meets a process that has ended. Without a job, TerminateProcess then
// answers "Access is denied.". Build 89cb6289 logged that text for the escalation of
// process.killAndWait. Kids of the child held its stdout and stderr there.
type procGroup struct {
	mu  sync.Mutex
	h   windows.Handle // 0 once closed, or after a failed open
	job windows.Handle // 0 with no job, once closed, or after a failed join
}

// spawnSysProcAttr is newSysProcAttr for process.spawn. With jobs on the child starts
// suspended, so it joins its job before it can start a process (confineProcess), and
// resumeChild lets it run. The suspended start is claustrum's own code, not measured.
func spawnSysProcAttr(jobs childJobs) *syscall.SysProcAttr {
	attr := newSysProcAttr()
	if jobs != jobsOff {
		attr.CreationFlags |= windows.CREATE_SUSPENDED
	}
	return attr
}

// confineProcess opens the kill handle of the just-started child and, with jobs on,
// puts the child in a new job. It always returns a non-nil *procGroup. If the open
// fails, the group has a zero handle, and signal() falls back to proc.Kill. If the job
// cannot be made or joined, the group has no job, and a kill ends the direct child
// only (claustrum's own, not measured). Neither fails the spawn. os.Process keeps its
// own handle privately, so the process is opened again by pid. That pid cannot be
// reused while os.Process holds its handle. The join takes a second handle, so a
// failed open of that one keeps the kill handle.
func confineProcess(proc *os.Process, jobs childJobs) (*procGroup, error) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(proc.Pid))
	if err != nil {
		return &procGroup{}, err
	}
	g := &procGroup{h: h}
	if jobs == jobsOff {
		return g, nil
	}
	quota, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(proc.Pid))
	if err != nil {
		return g, err
	}
	defer func() { _ = windows.CloseHandle(quota) }()
	job, err := newChildJob()
	if err != nil {
		return g, err
	}
	if err := windows.AssignProcessToJobObject(job, quota); err != nil {
		_ = windows.CloseHandle(job)
		return g, os.NewSyscallError("AssignProcessToJobObject", err)
	}
	g.job = job
	return g, nil
}

// newChildJob makes the job of one child. Its one limit is
// JOB_OBJECT_LIMIT_BREAKAWAY_OK. The handle is not inheritable, so only the daemon
// holds it.
func newChildJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, os.NewSyscallError("CreateJobObject", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, os.NewSyscallError("SetInformationJobObject", err)
	}
	return job, nil
}

// resumeChild lets a child run that spawnSysProcAttr started suspended. It resumes
// each thread of the child. A suspended new process has its first thread only, and no
// thread of another process has its pid. A child that did not start suspended needs
// nothing. claustrum's own code, not measured.
func resumeChild(proc *os.Process, attr *syscall.SysProcAttr) error {
	if attr == nil || attr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		return nil
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return os.NewSyscallError("CreateToolhelp32Snapshot", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	te := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != uint32(proc.Pid) {
			continue
		}
		th, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if openErr != nil {
			return os.NewSyscallError("OpenThread", openErr)
		}
		_, resumeErr := windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if resumeErr != nil {
			return os.NewSyscallError("ResumeThread", resumeErr)
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return os.NewSyscallError("Thread32Next", err)
	}
	if resumed == 0 {
		return errors.New("no thread of the child")
	}
	return nil
}

// signal ends the job of the child, or the direct child when it has no job. Windows
// has no POSIX signals, so signame is ignored: every signal form ends the child the
// same way (89cb6289, with no job, see procGroup). It returns the error of
// TerminateJobObject, or of TerminateProcess. If TerminateJobObject fails, it ends the
// direct child with TerminateProcess (claustrum's own, not measured). With no handle it falls back
// to proc.Kill and returns nil. Nil-receiver safe.
func (g *procGroup) signal(proc *os.Process, _ string) error {
	if g != nil {
		g.mu.Lock()
		if g.job != 0 {
			err := windows.TerminateJobObject(g.job, 1)
			if err != nil && g.h != 0 {
				err = windows.TerminateProcess(g.h, 1)
				g.mu.Unlock()
				return os.NewSyscallError("TerminateProcess", err)
			}
			g.mu.Unlock()
			return os.NewSyscallError("TerminateJobObject", err)
		}
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

// endRest ends what is left in the job of a child that has ended. The child itself is
// gone, so only processes that it started remain there. A no-op with no job.
// Nil-receiver safe.
func (g *procGroup) endRest() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job != 0 {
		_ = windows.TerminateJobObject(g.job, 1)
	}
}

// close releases the kill handle and the job handle. It ends nothing. Nil-receiver
// safe and idempotent.
func (g *procGroup) close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job != 0 {
		_ = windows.CloseHandle(g.job)
		g.job = 0
	}
	if g.h != 0 {
		_ = windows.CloseHandle(g.h)
		g.h = 0
	}
}

// logsUnreapedKill is false: Windows logs no line for a child that the kill did not
// reap in killReapGrace, as before (claustrum's own, not measured).
const logsUnreapedKill = false
