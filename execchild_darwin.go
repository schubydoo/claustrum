//go:build darwin

package main

import "os"

// ownStartToken returns this process's start-time token for the CLAUDE_SSH_CHILD marker on
// darwin: the process start-time as a UTC ANSIC timestamp (the value `ps -o lstart` prints
// under TZ=UTC). It is the same value the child registry records for this process, so the
// reap's CLAUDE_SSH_CHILD marker check compares equal. The shared trampoline logic lives in
// execchild_unix.go.
func ownStartToken() string {
	return darwinProcStart(os.Getpid())
}
