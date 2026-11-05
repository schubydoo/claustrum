//go:build linux || darwin

package procinfo

import (
	"syscall"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// KillGroup signals the whole process group led by pid through the killGroup seam. A
// pid below 2 gets the error of sysproc.RefuseSmallPid and no call of the seam.
func KillGroup(pid int, sig syscall.Signal) error {
	if err := sysproc.RefuseSmallPid(pid); err != nil {
		return err
	}
	return killGroup(pid, sig)
}
