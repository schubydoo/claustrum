//go:build unix

package main

import (
	"os"
	"syscall"
)

// stopGitProcess sends SIGTERM to a git call at its time limit. A call that
// ignores it is killed later: the update-ref of the branch step after
// updateRefKillAfter, and the calls of gitRun after gitStopGrace. Measured
// against 89cb6289 on Linux and macOS and against 5fd08069.
func stopGitProcess(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
