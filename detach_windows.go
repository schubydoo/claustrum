//go:build windows

package main

import "syscall"

// DETACHED_PROCESS detaches the child from the parent's console; combined with a
// new process group this is the Windows analogue of setsid daemonization.
const detachedProcess = 0x00000008

func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP}
}
