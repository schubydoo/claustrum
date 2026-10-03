package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// readExecChildError consumes the exec-child trampoline's exec-error pipe after the
// wrapped command started: any bytes are the target's exec error (surfaced as the
// spawn error, matching a direct Start failure); EOF — the CLOEXEC close on a
// successful exec — means the target is running. It always closes the read end. The
// caller must close its own write end first, or this blocks past EOF. Cross-platform
// so spawn (in process.go) can call it unconditionally; off linux and darwin the trampoline
// is a no-op that returns no pipe, so this is never reached there.
func readExecChildError(r *os.File) error {
	defer r.Close()
	b, _ := io.ReadAll(r)
	if len(b) > 0 {
		return errors.New(string(b))
	}
	return nil
}

// execChildRunDir derives the run dir of the daemon from its socket path. The run dir
// is the folder of the socket path, cleaned lexically. It is not made absolute and no
// symlink is resolved. It is "" only for an empty socket path.
//
// The run dir is the CLAUDE_SSH_RUN_DIR value of a child, and the child records go to
// its children folder. Measured on Linux and macOS against 89cb6289 (rows F1 to F4 and
// K3): the socket <R>/b/s.sock gives <R>/b, the relative socket s.sock gives ".", and
// <R>//run/./c1/rpc.sock gives <R>/run/c1. The record of the child is in the children
// folder below that run dir in each row. No test of the socket shape decides it.
//
// Not measured: an abstract socket name (@name). It gives the run dir "." here, with
// no special case.
func execChildRunDir(socket string) string {
	if socket == "" {
		return ""
	}
	return filepath.Dir(socket)
}
