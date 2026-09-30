//go:build unix

package main

import (
	"os"
	"syscall"
)

// stopUpdateRef sends SIGTERM to the update-ref of the branch step at
// updateRefStop. A call that ignores it is killed updateRefKillAfter later.
// Measured against 89cb6289 on Linux and macOS VMs (rows R23a, R23b, R23a1 and
// R23b1). Only the git process gets the signal. Whether the references reach its
// children is not measured.
func stopUpdateRef(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
