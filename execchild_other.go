//go:build !linux && !darwin

package main

import (
	"os"
	"os/exec"
)

// The exec-child trampoline runs on the unix targets (execchild_unix.go, with the
// OS-specific start-time source in execchild_linux.go / execchild_darwin.go). On windows
// the reference links only a trampoline shim (no re-exec), so here these are no-ops: the
// daemon spawns the target directly, exactly as before. The windows shim is reconciled in
// the windows track of this slice.

// maybeRunExecChild is a no-op on windows — there is no --exec-child mode to intercept.
func maybeRunExecChild() {}

// wrapCmdWithTrampoline is a no-op on windows — the command is spawned directly, so it
// returns no exec-error pipe.
func wrapCmdWithTrampoline(*exec.Cmd, string) (execErrR, execErrW *os.File) { return nil, nil }
