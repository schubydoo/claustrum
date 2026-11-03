//go:build windows

package runlock

import "github.com/schubydoo/claustrum/internal/logging"

// LockRunFolder takes no lock on Windows. Measured on Windows, the reference (4534d86) writes no
// daemon.lock and evicts no predecessor. Windows relies on the socket remove-then-rebind handoff
// for mutual exclusion, where a second -serve leaves the incumbent alive. claustrum matches: it
// writes nothing here. Both funcs of the returned claim are no-ops.
//
// It prints one line. On the reference that line is the first log line of every start.
// Measured on Windows against f6010b97 and 89cb6289, in every measured start.
func LockRunFolder(socket, role string) Claim {
	logging.Infof("%s", NotHolderLine)
	return Claim{Release: func() {}, Complete: func(string, int64) {}, StopLate: func() {}}
}

// Claim is what LockRunFolder gives back. On Windows there is no lock, so Release,
// Complete and StopLate do nothing.
type Claim struct {
	Release  func()
	Complete func(instanceID string, startedAt int64)
	StopLate func()
	Holder   bool // never set: there is no lock
}

// StopRunDirHolder is the -stop fallback after a failed connect. Windows has no
// run-dir lock, so no daemon.lock can name a holder, and the word is always "none".
func StopRunDirHolder(socket string) (string, func()) { return StopWordNone, func() {} }

// StopAfterReply is the step of -stop after the read of the shutdown reply. Windows
// has no run-dir lock to wait for, so the word is "stopped" at once. 5fd08069 prints
// it at once too (measured on Windows).
func StopAfterReply(socket string) (string, func()) { return StopWordStopped, func() {} }
