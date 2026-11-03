//go:build linux

package procinfo

import "syscall"

// SetRootForTest points the /proc mount at dir and returns the old mount.
func SetRootForTest(dir string) (prev string) {
	prev, procRoot = procRoot, dir
	return prev
}

// ReadlinkForTest returns the current link read.
func ReadlinkForTest() func(string) (string, error) { return osReadlink }

// SetReadlinkForTest sets the link read and returns the old one.
func SetReadlinkForTest(f func(string) (string, error)) (prev func(string) (string, error)) {
	prev, osReadlink = osReadlink, f
	return prev
}

// PidfdOpenForTest returns the current pidfd_open call.
func PidfdOpenForTest() func(int) (int, error) { return hcPidfdOpen }

// SetPidfdOpenForTest sets the pidfd_open call and returns the old one.
func SetPidfdOpenForTest(f func(int) (int, error)) (prev func(int) (int, error)) {
	prev, hcPidfdOpen = hcPidfdOpen, f
	return prev
}

// PidfdSendForTest returns the current pidfd_send_signal call.
func PidfdSendForTest() func(int, syscall.Signal) error { return hcPidfdSend }

// SetPidfdSendForTest sets the pidfd_send_signal call and returns the old one.
func SetPidfdSendForTest(f func(int, syscall.Signal) error) (prev func(int, syscall.Signal) error) {
	prev, hcPidfdSend = hcPidfdSend, f
	return prev
}

// KillPidForTest returns the current kill of one process.
func KillPidForTest() func(int, syscall.Signal) error { return hcKillPid }

// SetKillPidForTest sets the kill of one process and returns the old one.
func SetKillPidForTest(f func(int, syscall.Signal) error) (prev func(int, syscall.Signal) error) {
	prev, hcKillPid = hcKillPid, f
	return prev
}
