//go:build windows

package main

// exitSignalName is empty on Windows: the wait path carries no POSIX signal, so
// the reference daemon omits the exit frame's signal field there while still
// emitting killedBy (measured on a Windows VM against 4534d86). Mirrors the unix
// build's contract.
func exitSignalName(waitErr error) string { return "" }
