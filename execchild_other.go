//go:build !linux && !darwin

package main

import (
	"os"
	"os/exec"
)

// The exec-child trampoline runs on the unix targets (execchild_unix.go, with the
// OS-specific start-time source in execchild_linux.go / execchild_darwin.go), where it
// stamps CLAUDE_SSH_RUN_DIR and a CLAUDE_SSH_CHILD identity on children spawned under a
// run-shaped socket. Measured on a windows VM: the 19f30c46 reference daemon reports it is
// not the run-dir lock holder on windows and stamps neither marker — a child spawned under a
// run-shaped socket has an environment byte-identical to one spawned under a bare socket.
// Claustrum holds no run-dir lock on windows either (claimRunDir is a no-op there, reference
// build 4534d86), so on windows these are no-ops and the daemon spawns the target directly.

// maybeRunExecChild is a no-op on windows — there is no --exec-child mode to intercept.
func maybeRunExecChild() {}

// wrapCmdWithTrampoline is a no-op on windows — the command is spawned directly, so it
// returns no exec-error pipe and adds no environment markers.
func wrapCmdWithTrampoline(*exec.Cmd, string) (execErrR, execErrW *os.File) { return nil, nil }
