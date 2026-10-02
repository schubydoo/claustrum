//go:build windows

package main

// claimRunDir takes no lock on Windows. Measured on a Windows VM, the reference (4534d86)
// writes no daemon.lock and evicts no predecessor. Windows relies on the socket remove-then-rebind
// handoff for mutual exclusion, where a second -serve leaves the incumbent alive.
// claustrum matches: it writes nothing here. The returned release func is a no-op.
//
// It prints one line. On the reference that line is the first log line of every start. A
// Windows VM measured it against f6010b97 and 89cb6289 (row K11 and every other daemon row).
func claimRunDir(socket, role, instanceID string) func() {
	logInfof("%s", runDirNotHolderLine)
	return func() {}
}

// stopRunDirHolder is the -stop fallback after a failed connect. Windows has no
// run-dir lock, so no daemon.lock can name a holder, and the word is always "none".
func stopRunDirHolder(socket string) (string, func()) { return stopWordNone, func() {} }
