//go:build unix

package main

import (
	"os"
	"syscall"
)

// stopGitProcess sends SIGTERM to a git call at its time limit. A call that
// ignores it is killed later: the update-ref of the branch step after
// updateRefKillAfter, and the calls of gitRun after gitStopGrace. Measured
// against 89cb6289 on Linux and macOS VMs (rows R23a, R23b, R23a1 and R23b1) and
// against 5fd08069 (cell T2 of round task010-01).
func stopGitProcess(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
