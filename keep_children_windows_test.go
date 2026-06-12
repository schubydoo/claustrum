//go:build windows

package main

import "testing"

// On Windows, -keep-children cannot be honored: children are confined to a Job
// Object created with KILL_ON_JOB_CLOSE, so the OS terminates them when the daemon
// exits regardless. honorKeepChildren must therefore force it OFF (with a warning,
// emitted as a side effect) rather than silently kill while claiming to keep.
func TestHonorKeepChildrenWindows(t *testing.T) {
	if honorKeepChildren(true) {
		t.Error("honorKeepChildren(true) = true, want false on Windows")
	}
	if honorKeepChildren(false) {
		t.Error("honorKeepChildren(false) = true, want false")
	}
}
