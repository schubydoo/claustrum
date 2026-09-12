//go:build unix

package main

import (
	"os"
	"os/exec"
	"testing"
)

// TestIgnoreSigpipeSurvivesClosedStdout exercises the REAL ignoreSigpipeDefault (not
// the dispatch seam TestSigpipeIgnoredForStdoutModes uses): a child that ignores
// SIGPIPE and then writes to a stdout whose reader has closed must survive — the write
// fails with EPIPE and the child exits 0 — rather than being killed by SIGPIPE. A
// no-op ignoreSigpipeDefault would let SIGPIPE terminate the child (a non-nil Wait
// error), so this fails against that mutant.
func TestIgnoreSigpipeSurvivesClosedStdout(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sigpipe"})
	cmd.Stdout = w
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Drop both ends in the parent, so the child's stdout (its own copy of w) has no
	// reader anywhere; then release the child, which now writes into the closed pipe.
	_ = w.Close()
	_ = r.Close()
	_, _ = stdin.Write([]byte("go\n"))
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child was killed writing to a closed stdout — SIGPIPE was not ignored: %v", err)
	}
}
