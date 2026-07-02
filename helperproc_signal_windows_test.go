//go:build windows

package main

// ignoreSigterm is a no-op on Windows: the "ignore-term" helper mode and its
// escalation test are Unix-only (whole-tree teardown there goes through the Job
// Object, not POSIX signals), so this only exists to satisfy the shared
// helperproc reference on the Windows build.
func ignoreSigterm() {}
