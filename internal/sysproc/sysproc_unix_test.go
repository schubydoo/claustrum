//go:build unix

package sysproc

import "testing"

// ReapProcessGroup is nil-safe: a command that never started has
// a nil cmd.Process, and reading its Pid for the pgid would panic inside the drain
// path, so the guard must come first.
func TestReapProcessGroupNilIsNoOp(t *testing.T) {
	ReapProcessGroup(nil)
}
