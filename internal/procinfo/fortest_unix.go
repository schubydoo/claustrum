//go:build linux || darwin

package procinfo

import "syscall"

// The functions below let the tests of package main swap the seams of this package.
// Only tests call them. Each getter returns the seam itself, not KillGroup, so a stand-in
// can call the value that it replaced. Each setter returns the value that it replaced.

// KillGroupForTest returns the current group signal.
func KillGroupForTest() func(int, syscall.Signal) error { return killGroup }

// SetKillGroupForTest sets the group signal and returns the old one.
func SetKillGroupForTest(f func(int, syscall.Signal) error) (prev func(int, syscall.Signal) error) {
	prev, killGroup = killGroup, f
	return prev
}
