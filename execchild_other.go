//go:build !linux

package main

import (
	"os"
	"os/exec"
)

// The exec-child trampoline is reproduced on linux only for now (execchild_linux.go).
// The reference links the same subsystem on darwin (with a different, inferred
// start-time source for the CLAUDE_SSH_CHILD marker) and links a trampoline shim on
// Windows whose behavior is not yet analyzed; both are separate follow-up slices
// pending their own measurement. So on every non-linux target these are no-ops: the
// daemon spawns the target directly, exactly as before.

// maybeRunExecChild is a no-op off linux — there is no --exec-child mode to intercept.
func maybeRunExecChild() {}

// wrapCmdWithTrampoline is a no-op off linux — the command is spawned directly, so it
// returns no exec-error pipe.
func wrapCmdWithTrampoline(*exec.Cmd, string) (execErrR, execErrW *os.File) { return nil, nil }
