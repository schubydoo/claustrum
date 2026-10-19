//go:build windows

package main

// claimRunDir takes no lock on Windows. Measured on a Windows VM, the reference (4534d86)
// writes no daemon.lock and evicts no predecessor. Windows relies on the socket remove-then-rebind
// handoff for mutual exclusion, where a second -serve leaves the incumbent alive.
// claustrum matches: it writes nothing here. Both funcs of the returned claim are no-ops.
//
// It prints one line. On the reference that line is the first log line of every start. A
// Windows VM measured it against f6010b97 and 89cb6289 (row K11 and every other daemon row).
func claimRunDir(socket, role string) runDirClaim {
	logInfof("%s", runDirNotHolderLine)
	return runDirClaim{release: func() {}, complete: func(string, int64) {}, stopLate: func() {}}
}

// runDirClaim is what claimRunDir gives back. On Windows there is no lock, so release,
// complete and stopLate do nothing.
type runDirClaim struct {
	release  func()
	complete func(instanceID string, startedAt int64)
	stopLate func()
	holder   bool // never set: there is no lock
}

// stopRunDirHolder is the -stop fallback after a failed connect. Windows has no
// run-dir lock, so no daemon.lock can name a holder, and the word is always "none".
func stopRunDirHolder(socket string) (string, func()) { return stopWordNone, func() {} }

// stopAfterReply is the step of -stop after the read of the shutdown reply. Windows
// has no run-dir lock to wait for, so the word is "stopped" at once. 5fd08069 prints
// it at once too (cell W-S1 of round task034-01, Windows VM).
func stopAfterReply(socket string) (string, func()) { return stopWordStopped, func() {} }
