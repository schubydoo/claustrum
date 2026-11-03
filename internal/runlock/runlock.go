// Package runlock holds the run-dir lock of a -serve daemon and the -stop steps
// that read it.
package runlock

// NotHolderLine is the line of a daemon that does not hold the run-dir lock. The text
// is the reference's (f6010b97 and 89cb6289). On Windows no daemon holds such a lock, and
// the line is the first line of every start (measured). On Linux the reference printed it
// for a lock that another process holds (measured). See LockRunFolder.
const NotHolderLine = "[daemon] serve: not the run dir's lock holder; leaving any predecessor's children alone and recording none"

// The -stop stdout words. -stop prints exactly one of them, then a newline, and
// exits 0 in every state that these words name. Measured against f6010b97 and
// 90fca6e6 on Linux.
const (
	StopWordStopped    = "stopped"    // the connect succeeded. On Linux and macOS daemon.lock also came free within 3 s (5fd08069, measured), or there is no usable one (claustrum's own)
	StopWordNone       = "none"       // the connect failed and daemon.lock is free or missing. claustrum's own rule: also when the lock cannot be opened or locked
	StopWordTerminated = "terminated" // the lock holder exited after SIGTERM
	StopWordKilled     = "killed"     // the lock holder ignored SIGTERM and got SIGKILL
	// StopWordSurvivor: the lock holder is a live process that -stop does not
	// signal, or the lock is still held 1.5 s after SIGKILL. claustrum prints it
	// too when a signal cannot reach a holder that is still there.
	StopWordSurvivor = "survivor"
)
