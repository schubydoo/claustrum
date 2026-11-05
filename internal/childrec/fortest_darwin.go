//go:build darwin

package childrec

// SetDarwinProcStartForTest sets the start-time read of darwin and returns the old one.
// Only tests of package main call it.
func SetDarwinProcStartForTest(f func(pid int) string) (prev func(pid int) string) {
	prev, darwinProcStart = darwinProcStart, f
	return prev
}
