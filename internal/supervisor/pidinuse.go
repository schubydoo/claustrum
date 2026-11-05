package supervisor

// PidInUse reports whether a process is at pid, by the test of pidInUse of the
// system (sysproc_unix.go, sysproc_windows.go). It sends no signal.
func PidInUse(pid int) bool { return pidInUse(pid) }
