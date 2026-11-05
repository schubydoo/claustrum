//go:build linux

package supervisor

import (
	"strconv"

	"github.com/schubydoo/claustrum/internal/childrec"
)

// The linux-specific start-time source for the exec-child trampoline's CLAUDE_SSH_CHILD
// marker. The shared trampoline logic lives in execchild_unix.go. childrec.OwnStartTicks
// is also used by the linux child registry (internal/childrec/childrecord_linux.go).

// ownStartToken returns this process's start-time token for the CLAUDE_SSH_CHILD marker on
// linux: field 22 of /proc/self/stat (clock ticks since boot) as a decimal string.
func ownStartToken() string {
	return strconv.FormatInt(childrec.OwnStartTicks(), 10)
}
