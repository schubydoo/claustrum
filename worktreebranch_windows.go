//go:build windows

package main

import "os"

// stopGitProcess kills a git call at its time limit. On a Windows VM 89cb6289
// ended the update-ref of the branch step about 4.98 s after its start (row
// R23a). 5fd08069 ended a held call at its limit with no wait (cells T1, T3h and
// T5 of round task010-01). Both showed exit code 1 and no signal. Go's
// Process.Kill gives that picture there.
func stopGitProcess(p *os.Process) error {
	return p.Kill()
}
