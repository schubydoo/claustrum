//go:build linux || darwin

package main

import (
	"errors"
	"io"
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
	stubExecChildExit(t)
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

// stubExecChildExit swaps execChildExit for the panicking stand-in of stubOsExit, so
// catchExit reports the exit code of the trampoline.
func stubExecChildExit(t *testing.T) {
	t.Helper()
	old := execChildExit
	execChildExit = func(code int) { panic(exitPanic{code}) }
	t.Cleanup(func() { execChildExit = old })
}

// TestExecChildTrampolineExitsWith127 runs the trampoline through TestMain in a copy of
// this test binary, with the real execChildExit. A target that cannot be exec'd and a
// short argv both end the copy with exit 127. The first one also writes its
// "fork/exec <path>: <err>" text to the exec-error pipe at fd 3.
func TestExecChildTrampolineExitsWith127(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const target = "/nonexistent/claustrum-trampoline-target"
	for _, tc := range []struct {
		args    []string
		wantErr string
	}{
		{[]string{execChildFlag, target, "target"}, "fork/exec " + target + ": no such file or directory"},
		{[]string{execChildFlag, target}, ""},
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(self, tc.args...)
		cmd.ExtraFiles = []*os.File{w}
		runErr := cmd.Run()
		_ = w.Close()
		got, _ := io.ReadAll(r)
		_ = r.Close()
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) || ee.ExitCode() != 127 {
			t.Errorf("%v: err %v, want exit status 127", tc.args, runErr)
		}
		if string(got) != tc.wantErr {
			t.Errorf("%v: exec-error pipe %q, want %q", tc.args, got, tc.wantErr)
		}
	}
}
