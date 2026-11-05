package supervisor

import "os"

// ChildJobsOffEnv is the off switch of the Windows child jobs. The daemon reads it at
// its start and removes it from its own environment. So no child gets the daemon value.
//
// Measured against 52d82f0a on Linux, macOS and Windows: with CLAUDE_SSH_NO_JOBS=1 in
// the environment of the daemon, the env of a child has no CLAUDE_SSH_NO_JOBS. Build
// c8e2f083 passed it on. Not measured: a value in the env of the request. That value
// reaches the child, as for CLAUDE_SSH_PEER_CHECK (claustrum's own).
const ChildJobsOffEnv = "CLAUDE_SSH_NO_JOBS"

// ChildJobs tells how spawn puts a child in a Windows job. Linux and macOS have no
// jobs and always use JobsOff.
type ChildJobs uint8

const (
	// JobsOff puts a child in no job. On Windows a kill ends the direct child only.
	JobsOff ChildJobs = iota
	// JobsOn puts each child in a job of its own, as 52d82f0a does (measured on Windows).
	// The job ends its processes at an explicit end and at the close of its last handle,
	// so a daemon that dies takes the trees with it (see ProcGroup).
	JobsOn
	// JobsKeep is JobsOn for -keep-children. The close of the last handle ends nothing,
	// so a kept child outlives the daemon (claustrum's own).
	JobsKeep
)

// StartChildJobs reads the off switch, removes it from the environment of the daemon
// and gives the job mode of the children. The switch is on only for the exact text
// "1". Other values are not measured, and claustrum takes them as no switch, as for
// CLAUDE_SSH_PEER_CHECK (claustrum's own). keep is -keep-children. On Windows it logs
// one line.
func StartChildJobs(keep bool) ChildJobs {
	off := os.Getenv(ChildJobsOffEnv) == "1"
	_ = os.Unsetenv(ChildJobsOffEnv)
	mode := JobsOn
	if keep {
		mode = JobsKeep
	}
	if off {
		mode = JobsOff
	}
	return childJobsAtStart(off, mode)
}
