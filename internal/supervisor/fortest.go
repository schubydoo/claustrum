package supervisor

// The functions below let the tests of package main swap the seams of this package.
// Only tests call them. Each setter returns the value that it replaced.

// SetPidInUseForTest sets the test of PidInUse and returns the old one.
func SetPidInUseForTest(f func(pid int) bool) (prev func(pid int) bool) {
	prev, pidInUse = pidInUse, f
	return prev
}
