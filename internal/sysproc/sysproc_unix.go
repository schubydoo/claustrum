//go:build unix

package sysproc

import (
	"os"
	"syscall"
)

// NewSysProcAttr puts a spawned child in its own process group so the whole
// subtree can be signalled together.
func NewSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// ReapProcessGroup SIGKILLs the whole process group led by proc — kill(-pgid) —
// reaping any descendant proc left behind. It is how git.worktree_create's checkout
// tears down a smudge/hook orphan that outlived git and kept the daemon's output
// pipe open past the drain cap, matching 4534d86's observed descendant reap. The command must
// have been started with NewSysProcAttr (Setpgid), so proc is its group's leader
// and its pid equals the pgid. Best-effort and nil-safe: a nil or already-reaped
// process is a no-op. (The pgid stays reserved while any group member is alive, so
// this is safe to call after proc itself has been waited on.)
func ReapProcessGroup(proc *os.Process) {
	if proc == nil {
		return
	}
	_ = syscall.Kill(-proc.Pid, syscall.SIGKILL)
}

// KillChildGroup sends SIGKILL to the process group pgid. A var so a test can see the
// call and give it an error.
var KillChildGroup = func(pgid int) error { return syscall.Kill(-pgid, syscall.SIGKILL) }

// SignalOne sends sig to the single process proc through its process handle. On a
// Linux kernel with pid file descriptors, the handle of a child that this daemon
// started holds one, and the standard library sends pidfd_send_signal on it. On a
// kernel without them, and on macOS, it sends kill(pid, sig). A handle whose process
// was waited for already sends nothing. A var so a test can see the call.
var SignalOne = func(proc *os.Process, sig syscall.Signal) error { return proc.Signal(sig) }
