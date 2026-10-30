//go:build windows

package main

// childJobsLine is the start line of the job mode. Measured on a Windows VM against
// 52d82f0a: it comes after the "not the run dir's lock holder" line and before the
// listening line. It ends "true (off switch: false)", and with CLAUDE_SSH_NO_JOBS=1
// "false (off switch: true)". Build c8e2f083 logs no such line.
const childJobsLine = "[daemon] job objects for child processes as the socket opens: %t (off switch: %t)"

// childJobsAtStart logs the job line and gives the job mode of the children. Jobs are
// on unless the switch is set.
func childJobsAtStart(off bool, mode childJobs) childJobs {
	logInfof(childJobsLine, !off, off)
	return mode
}
