//go:build unix

package main

import "syscall"

// detachSysProcAttr starts the daemonized child in a new session (reparented to
// init), detaching it from the controlling terminal.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
