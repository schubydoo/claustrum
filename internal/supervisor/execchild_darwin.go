//go:build darwin

package supervisor

import (
	"os"

	"github.com/schubydoo/claustrum/internal/childrec"
)

// ownStartToken returns this process's start-time token for the CLAUDE_SSH_CHILD marker on
// darwin: the process start-time as a UTC date text with one blank between its words
// (childrec.DarwinProcStart). It is the same value the child registry records for this process, so the
// reap's CLAUDE_SSH_CHILD marker check compares equal. The shared trampoline logic lives in
// execchild_unix.go.
func ownStartToken() string {
	return childrec.DarwinProcStart(os.Getpid())
}
