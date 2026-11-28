//go:build windows

package supervisor

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestWrapCmdWithTrampolineWindowsInert pins the verified windows contract: the daemon
// injects no environment markers on windows. Measured on Windows, the 19f30c46 reference
// daemon reports it is not the run-dir lock holder on windows and stamps neither
// CLAUDE_SSH_RUN_DIR nor CLAUDE_SSH_CHILD (a child's env under a run-shaped socket matched a
// bare-socket child's). WrapCmdWithTrampoline returns no exec-error pipe and leaves an
// env with no marks unchanged, even with a run dir set. A mutant that injected a marker on windows
// would fail here.
func TestWrapCmdWithTrampolineWindowsInert(t *testing.T) {
	cmd := exec.Command(`C:\Windows\System32\cmd.exe`, "/c", "echo hi")
	cmd.Env = []string{"FOO=bar", `PATH=C:\Windows\System32`}
	before := append([]string(nil), cmd.Env...)

	r, w := WrapCmdWithTrampoline(cmd, `C:\x\run\c1`)

	if r != nil || w != nil {
		t.Errorf("WrapCmdWithTrampoline returned a pipe on windows; it must return (nil, nil)")
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
			t.Errorf("WrapCmdWithTrampoline injected a marker on windows: %q", e)
		}
	}
}

// TestWrapCmdWithTrampolineWindowsDropsMarks: a
// CLAUDE_SSH_CHILD and a CLAUDE_SSH_RUN_DIR in the env of a spawn reach no child, on
// 87375cc7 and on 13047a51 (measured on Windows). The other entries keep their order,
// CLAUDE_SSH_DAEMON_CHILD included. A nil env stays nil, so the child inherits.
func TestWrapCmdWithTrampolineWindowsDropsMarks(t *testing.T) {
	cmd := exec.Command(`C:\Windows\System32\cmd.exe`, "/c", "set")
	cmd.Env = []string{"FOO=bar", EnvChildMarker + "=y", "CLAUDE_SSH_DAEMON_CHILD=1", EnvRunDir + `=C:\w\y`, "ZED=1"}

	if r, w := WrapCmdWithTrampoline(cmd, `C:\x\run\c1`); r != nil || w != nil {
		t.Errorf("WrapCmdWithTrampoline returned a pipe on windows; it must return (nil, nil)")
	}
	if want := []string{"FOO=bar", "CLAUDE_SSH_DAEMON_CHILD=1", "ZED=1"}; !slices.Equal(cmd.Env, want) {
		t.Errorf("cmd.Env = %q, want %q", cmd.Env, want)
	}

	bare := exec.Command(`C:\Windows\System32\cmd.exe`, "/c", "set")
	WrapCmdWithTrampoline(bare, `C:\x\run\c1`)
	if bare.Env != nil {
		t.Errorf("a nil env became %q, want it to stay nil", bare.Env)
	}
}
