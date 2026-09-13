//go:build !linux && !darwin

package main

// recordChild is a no-op on the remaining platforms (windows and others). Linux and
// darwin have their own identity gathers (childrecord_linux.go / childrecord_darwin.go);
// the reference also records children on windows, which is reconciled in the windows
// track of this slice.
func (m *procManager) recordChild(pid int, argv0 string) {}
