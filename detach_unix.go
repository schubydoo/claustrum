//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// detachSysProcAttr starts the daemonized child in a new session (reparented to
// init), detaching it from the controlling terminal.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// startDetached starts the daemonized child. On unix it is cmd.Start. Windows has its own.
func startDetached(cmd *exec.Cmd) error { return cmd.Start() }

// openHeldDaemonLog is nil on unix. A log that cannot be rotated there is the foreign file
// or symlink of D8, and the launcher falls back to its inherited stdio. Windows has its own.
func openHeldDaemonLog(path string) *os.File { return nil }
