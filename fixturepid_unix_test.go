//go:build linux || darwin

package main

import (
	"errors"
	"syscall"
	"testing"
)

// TestFakePidNamesNoGroup pins that noSuchPid, the pid of the fake processes behind the
// signalGroup seam, names no process group. The bound check runs first, so a small value
// fails the test before any kill call. The kill sends signal 0, which delivers nothing.
func TestFakePidNamesNoGroup(t *testing.T) {
	// 1<<22 is the pid limit of Linux. The limit of macOS is lower.
	if noSuchPid <= 1<<22 {
		t.Fatalf("noSuchPid = %d, want a value above the pid limit 1<<22", noSuchPid)
	}
	if err := syscall.Kill(-noSuchPid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill(-noSuchPid, 0) = %v, want ESRCH", err)
	}
}
