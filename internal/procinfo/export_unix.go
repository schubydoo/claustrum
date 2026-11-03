//go:build linux || darwin

package procinfo

import "syscall"

// KillGroup signals the whole process group led by pid through the killGroup seam.
func KillGroup(pid int, sig syscall.Signal) error { return killGroup(pid, sig) }
