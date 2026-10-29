package main

import (
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"syscall"
)

// spawnReason is the data member of an error that the start path of process.spawn
// makes. The member order is code, message, data.
type spawnReason struct {
	Reason string `json:"reason"`
}

// cwdError marks the two cwd refusals of the start path (missing, or not a folder).
type cwdError struct{ err error }

func (e *cwdError) Error() string { return e.err.Error() }
func (e *cwdError) Unwrap() error { return e.err }

// spawnFailureReason gives the reason of a refusal that spawnVia returned.
// Measured on Linux and on Windows: cwd_missing, cwd_unusable (mode 000 cwd, Linux
// and macOS), launcher, not_found, not_executable, other. The reasons on macOS other
// than cwd_unusable, the -32005 launcher start refusal, a pipe failure and an errno
// that no row lists are claustrum's own, not measured.
func spawnFailureReason(err error, cwd string) spawnReason {
	var sr *spawnRefusal
	if errors.As(err, &sr) {
		if sr.code == codeInvalidParam {
			return spawnReason{"other"}
		}
		return spawnReason{"launcher"}
	}
	var ce *cwdError
	if errors.As(err, &ce) {
		return spawnReason{"cwd_missing"}
	}
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist),
		strings.HasSuffix(err.Error(), ": "+syscall.ENOENT.Error()):
		return spawnReason{"not_found"}
	case errors.Is(err, fs.ErrPermission), strings.HasSuffix(err.Error(), ": "+syscall.EACCES.Error()):
		// The cwd is tested again after the failed start. A cwd that exists but
		// cannot be entered gives cwd_unusable. Measured for mode 000 only.
		if cwd != "" && cwdUnusable(cwd) {
			return spawnReason{"cwd_unusable"}
		}
		return spawnReason{"not_executable"}
	}
	return spawnReason{"other"}
}
