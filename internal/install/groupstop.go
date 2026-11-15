package install

import (
	"os/exec"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// reapRunGroup is the group kill of a bounded run of -install and -probe-cli.
// A test replaces it.
var reapRunGroup = sysproc.ReapProcessGroup

// leaderExitWait is the wait for the exit of a bounded run (sysproc.AwaitExit).
// A test replaces it to see a wait that fails.
var leaderExitWait = sysproc.AwaitExit

// runWaited runs right after the wait of a bounded run. A test sets it to run a
// stop late, as the watch of the context of Go can.
var runWaited = func(*exec.Cmd) {}

// groupStop is the cmd.Cancel of a bounded run. It sends the group kill through
// gate and then kills the leader through its handle. The handle of a reaped
// leader sends nothing.
func groupStop(cmd *exec.Cmd, gate *sysproc.LeaderGate) func() error {
	return func() error {
		gate.Do(func() { reapRunGroup(cmd.Process) })
		_ = cmd.Process.Kill()
		return nil
	}
}

// runGated starts cmd and waits for it. gate closes after the exit of the
// leader and before the wait reaps it. Go runs cmd.Cancel from its own watch of
// the context, and that call can come after the reap. The gate keeps a late
// stop from a group kill of a free group number (claustrum's own, from the
// code).
func runGated(cmd *exec.Cmd, gate *sysproc.LeaderGate) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	gate.CloseAfterExit(cmd.Process, leaderExitWait(cmd.Process.Pid))
	err := cmd.Wait()
	runWaited(cmd)
	return err
}
