//go:build windows

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestWrapCmdWithTrampolineWindowsInert pins the verified windows contract: the daemon
// injects no environment markers on windows. Measured on a windows VM, the 19f30c46 reference
// daemon reports it is not the run-dir lock holder on windows and stamps neither
// CLAUDE_SSH_RUN_DIR nor CLAUDE_SSH_CHILD (a child's env under a run-shaped socket matched a
// bare-socket child's). wrapCmdWithTrampoline returns no exec-error pipe and leaves the
// command's env unchanged even with a run dir set. A mutant that injected a marker on windows
// would fail here.
func TestWrapCmdWithTrampolineWindowsInert(t *testing.T) {
	cmd := exec.Command(`C:\Windows\System32\cmd.exe`, "/c", "echo hi")
	cmd.Env = []string{"FOO=bar", `PATH=C:\Windows\System32`}
	before := append([]string(nil), cmd.Env...)

	r, w := wrapCmdWithTrampoline(cmd, `C:\x\run\c1`)

	if r != nil || w != nil {
		t.Errorf("wrapCmdWithTrampoline returned a pipe on windows; it must return (nil, nil)")
	}
	if len(cmd.Env) != len(before) {
		t.Fatalf("cmd.Env length changed: got %d, want %d", len(cmd.Env), len(before))
	}
	for i := range before {
		if cmd.Env[i] != before[i] {
			t.Errorf("cmd.Env[%d] changed: got %q, want %q", i, cmd.Env[i], before[i])
		}
	}
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "CLAUDE_SSH_RUN_DIR=") || strings.HasPrefix(e, "CLAUDE_SSH_CHILD=") {
			t.Errorf("wrapCmdWithTrampoline injected a marker on windows: %q", e)
		}
	}
}
