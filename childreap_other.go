//go:build !linux && !darwin

package main

// reapOrphans is a no-op on the remaining platforms (windows and others). Linux and darwin
// have their own live-process readers (childreap_linux.go / childreap_darwin.go) over the
// shared decision logic in childreap.go; the reference also reaps on windows, which is
// reconciled in the windows track of this slice.
func reapOrphans(runDir, ownInstance string) {}
