//go:build windows

package main

import (
	"os"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// newSysProcAttr puts the child in a new process group (Windows has no setpgid;
// CREATE_NEW_PROCESS_GROUP is the closest analogue). The whole-tree teardown is
// handled by the Job Object set up in confineProcess, not by the process group.
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// honorKeepChildren forces -keep-children OFF on Windows. Children are confined to
// a Job Object created with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE (see confineProcess);
// the daemon holds that job handle, so when it exits the OS terminates the whole
// tree regardless of any shutdown-time decision. Rather than silently kill while
// claiming to keep, we ignore the flag and warn. (The hosted channel that uses
// this is POSIX-only anyway.)
func honorKeepChildren(requested bool) bool {
	if requested {
		logWarnf("[Server] -keep-children is not supported on Windows and is ignored: child processes are confined to a Job Object that the OS terminates when the daemon exits")
	}
	return false
}

// procGroup confines a spawned child — and everything it spawns — to a Windows
// Job Object, so the entire tree can be torn down at once. This is the analogue
// of a Unix process-group kill: the previous best-effort TerminateProcess hit
// only the parent, leaking any grandchildren.
//
// The job is created with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, so closing the
// handle (on the child's exit, or if the daemon itself dies) reaps any lingering
// descendants instead of orphaning them.
type procGroup struct {
	mu  sync.Mutex
	job windows.Handle // 0 once closed, or if confinement failed
}

// confineProcess creates a Job Object and assigns the just-started child to it.
// It always returns a non-nil *procGroup: on any failure the group has a zero
// job handle, and signal() falls back to killing just the parent — preserving
// the previous best-effort behavior rather than failing the spawn.
//
// There is a small unavoidable race: the child is assigned to the job just after
// CreateProcess returns, so a grandchild spawned in that window could escape.
// os/exec doesn't expose the suspended main thread, so CREATE_SUSPENDED + resume
// isn't available to us; in practice the window is negligible.
func confineProcess(proc *os.Process) (*procGroup, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return &procGroup{}, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return &procGroup{}, err
	}
	// AssignProcessToJobObject needs a handle carrying these rights; os.Process
	// keeps its own handle privately, so reopen the process by pid.
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(proc.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return &procGroup{}, err
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		_ = windows.CloseHandle(job)
		return &procGroup{}, err
	}
	return &procGroup{job: job}, nil
}

// signal terminates the entire job — the child and every descendant. Windows has
// no POSIX signals, so signame is ignored (matching the previous proc.Kill()
// behavior). If the job was never established, it falls back to killing just the
// parent. Nil-receiver safe.
func (g *procGroup) signal(proc *os.Process, _ string) {
	if g == nil || !g.terminate() {
		_ = proc.Kill()
	}
}

// terminate kills the job tree under the lock and reports whether it did so.
func (g *procGroup) terminate() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 {
		return false
	}
	return windows.TerminateJobObject(g.job, 1) == nil
}

// close releases the job handle. With KILL_ON_JOB_CLOSE set, dropping the last
// handle also terminates any descendants still alive. Nil-receiver safe and
// idempotent.
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
}
