package main

import (
	"os"

	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/supervisor"
)

// leaveGroupsEnv keeps what an exited launcher leaves in its process group alive.
const leaveGroupsEnv = "CLAUDE_SSH_LEAVE_GROUPS"

// launcherExitFeature is the feature of the end of a launcher's group.
const launcherExitFeature = "launcher.exit_ends_group"

// startLeaveGroups reads the variable once, at the start of the daemon, on Linux and
// macOS. Only the exact value 1 turns the end of a launcher's group off, with one line. The
// values 1, 0, yes and empty leave the daemon environment, so no child inherits them (87375cc7,
// measured on Linux and macOS). Any other value leaves it too (claustrum's own, not measured).
// Windows keeps the variable as it is. A failed removal gets one WARN line (claustrum's own).
func startLeaveGroups(m *supervisor.Manager) {
	if !supervisor.LauncherGroupEndBuilt {
		return
	}
	leave := os.Getenv(leaveGroupsEnv) == "1"
	if err := unsetenv(leaveGroupsEnv); err != nil {
		logging.Warnf("[Server] cannot remove %s from the daemon environment (%v): child processes inherit it", leaveGroupsEnv, err)
	}
	if !leave {
		return
	}
	m.LeaveGroups = true
	logging.Infof("[daemon] what an exited launcher leaves in its process group lives on: %s is set", leaveGroupsEnv)
}
