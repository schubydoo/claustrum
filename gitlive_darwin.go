//go:build darwin

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// darwinZombie is the state SZOMB of a process in the kinfo_proc of macOS.
const darwinZombie = 5

// gitStillRuns reports that the git process p has not exited. A git that
// exited and is not yet reaped does not run. A test that fails counts as a
// git that runs (claustrum's own, not measured).
func gitStillRuns(p *os.Process) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", p.Pid)
	return err != nil || kp.Proc.P_stat != darwinZombie
}
