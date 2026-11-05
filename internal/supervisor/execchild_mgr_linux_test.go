//go:build linux

package supervisor

import (
	"errors"
	"os"
	"testing"
)

// TestSpawnClosesTrampolinePipeOnConstructionFailure proves spawn closes the
// trampoline's exec-error pipe when a later pre-Start step fails (greptile P2). Under
// a run dir a resolved command is wrapped, which opens that pipe; if the stdout pipe
// then fails, both ends must be closed, not leaked. Linux-only: only here does
// WrapCmdWithTrampoline open the pipe. The assertion is on the FILES (a second Close
// reports ErrClosed), matching TestSpawnClosesPipesOnConstructionFailure.
func TestSpawnClosesTrampolinePipeOnConstructionFailure(t *testing.T) {
	oldPipe := osPipe
	t.Cleanup(func() { osPipe = oldPipe })

	var errR, errW *os.File
	var oldErrPipe func() (*os.File, *os.File, error)
	oldErrPipe = SetExecChildErrPipeForTest(func() (*os.File, *os.File, error) {
		r, w, err := oldErrPipe()
		errR, errW = r, w
		return r, w, err
	})
	t.Cleanup(func() { SetExecChildErrPipeForTest(oldErrPipe) })
	// Fail the first stdout pipe, after the exec-error pipe was already opened.
	osPipe = func() (*os.File, *os.File, error) {
		return nil, nil, errors.New("pipe: too many open files")
	}
	m := newTestProcManager(t)
	m.RunDir = "/x/run/c0ffee01" // run-shaped -> a resolved command is trampolined
	if _, err := m.Spawn(nil, "P", "/bin/sh", []string{"-c", "true"}, "", nil, false); err == nil {
		t.Fatal("spawn succeeded despite a stdout pipe failure")
	}
	if errR == nil || errW == nil {
		t.Fatal("the trampoline exec-error pipe was never opened; the spawn was not wrapped")
	}
	closed := func(f *os.File) bool { return errors.Is(f.Close(), os.ErrClosed) }
	if !closed(errR) || !closed(errW) {
		t.Error("the trampoline exec-error pipe was left open after a pre-Start failure (fd leak)")
	}
}
