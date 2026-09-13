//go:build linux || darwin

package main

import (
	"os/exec"
	"testing"
)

// TestWrapCmdFallsBackWhenNoSelf covers the no-re-executable-self path: when trampolineSelf
// returns "" (the daemon's own binary is gone, e.g. uninstalled while running), the spawn
// must fall back to launching the target directly — no re-exec, no exec-error pipe — while
// still tagging the child with CLAUDE_SSH_RUN_DIR. This matches the reference, whose
// children after an uninstall keep the run-dir marker and only lose CLAUDE_SSH_CHILD. On
// linux trampolineSelf is /proc/self/exe (survives an unlink), so the real trigger is
// darwin; the seam exercises the shared decision here.
func TestWrapCmdFallsBackWhenNoSelf(t *testing.T) {
	old := trampolineSelf
	t.Cleanup(func() { trampolineSelf = old })
	trampolineSelf = func() string { return "" }

	cmd := exec.Command("/bin/echo", "hi")
	cmd.Env = []string{"PATH=/usr/bin"}
	r, w := wrapCmdWithTrampoline(cmd, "/opt/claude/run/c0ffee01")

	if r != nil || w != nil {
		t.Error("no-self fallback returned an exec-error pipe; want nil,nil (direct spawn)")
	}
	if cmd.Path != "/bin/echo" || len(cmd.Args) != 2 || cmd.Args[0] != "/bin/echo" {
		t.Errorf("cmd was rewritten to re-exec (path=%q args=%v); want an unchanged direct spawn", cmd.Path, cmd.Args)
	}
	if len(cmd.ExtraFiles) != 0 {
		t.Error("no-self fallback attached an extra fd; want none")
	}
	found := false
	for _, e := range cmd.Env {
		if e == "CLAUDE_SSH_RUN_DIR=/opt/claude/run/c0ffee01" {
			found = true
		}
	}
	if !found {
		t.Errorf("CLAUDE_SSH_RUN_DIR not set on the direct-spawn fallback: %v", cmd.Env)
	}
}
