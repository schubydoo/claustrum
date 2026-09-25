//go:build windows

package main

// claimRunDir is a no-op on Windows. Measured on a Windows VM, the reference (4534d86)
// writes no daemon.lock and evicts no predecessor. Windows relies on the socket remove-then-rebind
// handoff for mutual exclusion, where a second -serve leaves the incumbent alive.
// claustrum matches by doing nothing here. The returned release func is a no-op.
func claimRunDir(socket, role string) func() { return func() {} }

// stopRunDirHolder is the -stop fallback after a failed connect. Windows has no
// run-dir lock, so no daemon.lock can name a holder, and the word is always "none".
func stopRunDirHolder(socket string) string { return stopWordNone }
