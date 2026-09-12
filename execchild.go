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
// so spawn (in process.go) can call it unconditionally; off linux the trampoline is a
// no-op that returns no pipe, so this is never reached there.
func readExecChildError(r *os.File) error {
	defer r.Close()
	b, _ := io.ReadAll(r)
	if len(b) > 0 {
		return errors.New(string(b))
	}
	return nil
}

// execChildRunDir derives the daemon's run dir from its socket path, or "" when the
// socket is not the run/<clientId>/rpc.sock shape. It is the CLAUDE_SSH_RUN_DIR value
// stamped on trampolined children and the gate for the exec-child trampoline: a
// daemon whose socket is not run-shaped (a bare /tmp socket, a test socket) spawns
// children directly, with no trampoline and no run-dir marker. For socket
// <X>/run/<clientId>/rpc.sock the run dir is <X>/run/<clientId>.
func execChildRunDir(socket string) string {
	if socket == "" || filepath.Base(socket) != "rpc.sock" {
		return ""
	}
	dir := filepath.Dir(socket) // <X>/run/<clientId>
	if filepath.Base(filepath.Dir(dir)) != "run" {
		return ""
	}
	return dir
}
