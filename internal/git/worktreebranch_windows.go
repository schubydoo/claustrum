//go:build windows

package git

import "os"

// stopGitProcess kills a git call at its time limit. On Windows 89cb6289 ended
// the update-ref of the branch step about 4.98 s after its start (measured).
// 5fd08069 ended a held call at its limit with no wait (measured). Both showed
// exit code 1 and no signal. Go's Process.Kill gives that picture there.
func stopGitProcess(p *os.Process) error {
	return p.Kill()
}
