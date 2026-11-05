//go:build unix

package main

import (
	"syscall"
	"testing"
	"time"
)

func TestDetachSysProcAttr(t *testing.T) {
	if detachSysProcAttr() == nil {
		t.Error("detachSysProcAttr should return a non-nil SysProcAttr")
	}
}

// waitProcessGone polls signal-0 until the pid no longer exists (ESRCH).
func waitProcessGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return true // ESRCH: the process is gone
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
