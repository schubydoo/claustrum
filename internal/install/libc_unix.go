//go:build unix

package install

import (
	"context"
	"fmt"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// lddKillGrace is the WaitDelay on the probe. It caps the wait for ldd's output
// pipe to close after ldd itself exits. The output already read is then USED,
// and the process that still holds the pipe is not killed. Measured on Linux
// against f6010b97: an ldd that prints a musl banner, leaves a `sleep` child
// holding its stdout and exits at 0.5 to 4.5 s answers musl about 2 s after it
// exits on the reference. The child survives.
const lddKillGrace = 2 * time.Second

// errLddBound marks an ldd run that the bound killed while ldd itself still ran.
// It wraps context.DeadlineExceeded, which is what detectLibcWith tests for.
var errLddBound = fmt.Errorf("ldd killed at the bound: %w", context.DeadlineExceeded)

// runLddVersion runs `ldd --version` under ctx. ldd runs in its OWN process
// group. If ctx expires while ldd runs, the whole group is killed, not only ldd,
// and the run reports errLddBound. Measured: the reference runs the stub in its
// own group (pgid = the stub's pid), and a `sleep` child of the stub is gone
// right after the reference answers at 5.02 s.
//
// Cancel runs only if ctx expires before Wait sees ldd exit. After ldd exits,
// WaitDelay alone bounds the drain.
func runLddVersion(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ldd", "--version")
	// The folder is "/" and the environment stays unfiltered. Claustrum's own,
	// not measured.
	cmd.Dir = "/"
	cmd.SysProcAttr = sysproc.NewSysProcAttr()
	var killed atomic.Bool
	cmd.Cancel = func() error {
		killed.Store(true)
		sysproc.ReapProcessGroup(cmd.Process)
		_ = cmd.Process.Kill()
		return nil
	}
	cmd.WaitDelay = lddKillGrace
	out, err := cmd.CombinedOutput()
	if killed.Load() {
		return out, errLddBound
	}
	return out, err
}
