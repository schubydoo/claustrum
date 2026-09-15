//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"slices"
	"syscall"
	"testing"
)

// The two refusal arms of the exec-child trampoline. Both are cheap to reach and both
// matter: the first runs in a re-exec'd child where nothing else can report a problem, and
// the second decides whether a spawn goes ahead without its error pipe.

// TestRunExecChildRefusesShortArgv: the trampoline needs at least the exec path and the
// target's own argv[0]. Anything shorter is a caller bug, and it exits 127 rather than
// indexing past the end of the slice — which is what the mutant does.
func TestRunExecChildRefusesShortArgv(t *testing.T) {
	stubOsExit(t)
	for _, argv := range [][]string{nil, {}, {"/nonexistent/target"}} {
		code, exited := catchExit(func() { runExecChild(argv) })
		if !exited || code != 127 {
			t.Errorf("runExecChild(%v) = code %d exited %v, want exit 127", argv, code, exited)
		}
	}
}

// TestWrapCmdWithTrampolinePipeFailure: with no exec-error pipe the trampoline could not
// report a failed target exec, so the spawn falls back to launching the target directly.
// The command must come back UNCHANGED — a mutant that wrapped it anyway hands cmd.Start a
// nil ExtraFile and rewrites the path to the daemon's own binary.
func TestWrapCmdWithTrampolinePipeFailure(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	old := execChildErrPipe
	t.Cleanup(func() { execChildErrPipe = old })
	execChildErrPipe = func() (*os.File, *os.File, error) { return nil, nil, syscall.EMFILE }

	cmd := exec.Command(self, "--version")
	wantPath, wantArgs := cmd.Path, slices.Clone(cmd.Args)
	wantEnv := slices.Clone(cmd.Env)

	r, w := wrapCmdWithTrampoline(cmd, "/x/run/c0ffee01")
	if r != nil || w != nil {
		t.Errorf("wrapCmdWithTrampoline returned a pipe (%v, %v) although os.Pipe failed", r, w)
	}
	if cmd.Path != wantPath || !slices.Equal(cmd.Args, wantArgs) {
		t.Errorf("the command was rewritten without an error pipe: path=%q args=%v", cmd.Path, cmd.Args)
	}
	if len(cmd.ExtraFiles) != 0 {
		t.Errorf("ExtraFiles = %v, want none: there is no pipe to pass", cmd.ExtraFiles)
	}
	if !slices.Equal(cmd.Env, wantEnv) {
		t.Errorf("cmd.Env changed to %v, want it untouched", cmd.Env)
	}
}
