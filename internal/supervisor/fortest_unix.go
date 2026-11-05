//go:build linux || darwin

package supervisor

import "os"

// TrampolineSelfForTest returns the path that the daemon re-execs as the trampoline.
func TrampolineSelfForTest() string { return trampolineSelf() }

// SetTrampolineSelfForTest sets the source of the trampoline path and returns the old
// one.
func SetTrampolineSelfForTest(f func() string) (prev func() string) {
	prev, trampolineSelf = trampolineSelf, f
	return prev
}

// SetExecChildErrPipeForTest sets the maker of the exec-error pipe of the trampoline
// and returns the old one.
func SetExecChildErrPipeForTest(f func() (*os.File, *os.File, error)) (prev func() (*os.File, *os.File, error)) {
	prev, execChildErrPipe = execChildErrPipe, f
	return prev
}
