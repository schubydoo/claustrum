package sockroute

// SetProcSelfFDForTest sets the folder of the open handles of this process and
// returns the one before it, so a test of another package can take the /proc route
// away.
func SetProcSelfFDForTest(dir string) (old string) {
	old, procSelfFD = procSelfFD, dir
	return old
}
