//go:build windows

package sysproc

// ExitSignalName is empty on Windows: the wait path carries no POSIX signal, so
// the reference daemon omits the exit frame's signal field there while still
// emitting killedBy (measured on Windows against 4534d86). Mirrors the unix
// build's contract.
func ExitSignalName(waitErr error) string { return "" }
