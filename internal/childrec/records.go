// Package childrec holds the child records of the daemon and the orphan reap of its
// start: the record of each spawned child in <runDir>/children, its removal when the
// child ends, and the reap of the children that a gone daemon of the same run dir left.
// On windows each step is a no-op.
package childrec

import (
	"os"
	"sync"
)

// ChildrenDirName is the name of the folder of the records in the run dir.
const ChildrenDirName = childrenDirName

// Records is the run dir of the daemon and the state of its child records. The manager
// embeds it. RunDir, InstanceID and NoRecords are set once at daemon startup
// (newServerOnSocket) and are read-only after that. The handle on the run dir opens
// once: at HoldRunDir, or at the first record call when HoldRunDir did not run.
type Records struct {
	// RunDir is the run dir of the daemon (CLAUDE_SSH_RUN_DIR): the folder of its
	// socket path (execChildRunDir). It is "" only for a manager with no socket, as a
	// unit test builds one. When non-empty, spawn launches children through the
	// exec-child trampoline. When empty it spawns them directly.
	RunDir string

	// InstanceID is the daemon's per-boot instance id (== server.capabilities
	// instance_id), threaded in at startup alongside RunDir. It is stamped into each
	// child's orphan-registry record (childRecord.Instance) so a later daemon can tell
	// this daemon's children apart from its own.
	InstanceID string
	// NoRecords is set for a daemon that does not hold the run-dir lock: it writes
	// no child record. That is measured for a held flock (`5fd08069`, measured on
	// Linux).
	NoRecords bool

	// runRoot is the run dir, held open for the child records (linux and darwin, see
	// childrecord_unix.go). runRootOnce guards the one open. Unused on windows.
	runRootOnce sync.Once //nolint:unused // used in childrecord_unix.go only
	runRoot     *os.Root  //nolint:unused // used in childrecord_unix.go only
	runRootErr  error     //nolint:unused // used in childrecord_unix.go only
}
