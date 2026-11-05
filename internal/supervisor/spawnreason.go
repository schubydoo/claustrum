package supervisor

import (
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"syscall"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// SpawnReason is the data member of an error that the start path of process.spawn
// makes. The member order is code, message, data.
type SpawnReason struct {
	Reason string `json:"reason"`
}

// CwdError marks the two cwd refusals of the start path (missing, or not a folder).
type CwdError struct{ Err error }

func (e *CwdError) Error() string { return e.Err.Error() }
func (e *CwdError) Unwrap() error { return e.Err }

// SpawnFailureReason gives the reason of a refusal that spawnVia returned (build
// 52d82f0a). Measured on Linux and on Windows: cwd_missing, launcher, not_found and
// other. Measured on Linux only: not_executable, and cwd_unusable for a cwd of mode
// 000 (also on macOS). The reasons on macOS other
// than cwd_unusable, the -32005 launcher start refusal, a pipe failure and an errno
// that no row lists are claustrum's own, not measured.
func SpawnFailureReason(err error, cwd string) SpawnReason {
	var sr *SpawnRefusal
	if errors.As(err, &sr) {
		if sr.Code == rpc.CodeInvalidParam {
			return SpawnReason{"other"}
		}
		return SpawnReason{"launcher"}
	}
	var ce *CwdError
	if errors.As(err, &ce) {
		return SpawnReason{"cwd_missing"}
	}
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist),
		strings.HasSuffix(err.Error(), ": "+syscall.ENOENT.Error()):
		return SpawnReason{"not_found"}
	case errors.Is(err, fs.ErrPermission), strings.HasSuffix(err.Error(), ": "+syscall.EACCES.Error()):
		// The cwd is tested again after the failed start. A cwd that exists but
		// cannot be entered gives cwd_unusable. Measured for mode 000 only.
		if cwd != "" && cwdUnusable(cwd) {
			return SpawnReason{"cwd_unusable"}
		}
		return SpawnReason{"not_executable"}
	}
	return SpawnReason{"other"}
}
