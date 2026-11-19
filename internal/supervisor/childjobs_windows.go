//go:build windows

package supervisor

import "github.com/schubydoo/claustrum/internal/logging"

// childJobsLine is the start line of the job mode. Measured on Windows against
// 52d82f0a: it comes after the "not the run dir's lock holder" line and before the
// listening line. It ends "true (off switch: false)", and with CLAUDE_SSH_NO_JOBS=1
// "false (off switch: true)". Build c8e2f083 logs no such line.
const childJobsLine = "[daemon] job objects for child processes as the socket opens: %t (off switch: %t)"

// childJobsAtStart runs the start test of the jobs unless the switch is set, logs the
// job line and gives the job mode of the children and the test. The line says true
// only for a test that passed in its first 5 s (measured on Windows against 4c111e86:
// a failed and a pending test log "false (off switch: false)"). With the switch there
// is no test and no test line (measured).
func childJobsAtStart(off bool, mode ChildJobs) (ChildJobs, *JobTest) {
	var test *JobTest
	if !off {
		test = startJobTest()
	}
	logging.Infof(childJobsLine, !off && !test.Outside(), off)
	return mode, test
}
