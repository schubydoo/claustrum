//go:build windows

package main

import "os"

// stopUpdateRef kills the update-ref of the branch step at updateRefStop.
// On a Windows VM 89cb6289 ended the call about 4.98 s after its start (row R23a).
// It showed exit code 1 and no signal. Go's Process.Kill gives that picture there.
func stopUpdateRef(p *os.Process) error {
	return p.Kill()
}
