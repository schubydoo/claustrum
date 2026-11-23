package supervisor

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestSpawnHandsChildTheShellAgentSocket(t *testing.T) {
	unsetForTest(t, "SSH_AUTH_SOCK")
	calls := stubShellAgentSocket(t, "/tmp/login-agent.sock")
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, frames := pipeConn(t)
	printenv, env := helperCommand(t, "printenv")
	if _, err := m.Spawn(c, "agent", printenv, []string{"SSH_AUTH_SOCK"}, "", env, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := firstStdout(t, frames); got != "SSH_AUTH_SOCK=/tmp/login-agent.sock" {
		t.Errorf("child saw %q, want the login-shell socket", got)
	}
	if *calls != 1 {
		t.Errorf("lookup ran %d times, want 1", *calls)
	}
	waitExitFrame(t, frames)
}

func TestSpawnDisableShellAgentSocket(t *testing.T) {
	unsetForTest(t, "SSH_AUTH_SOCK")
	calls := stubShellAgentSocket(t, "/tmp/login-agent.sock")
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, frames := pipeConn(t)
	printenv, env := helperCommand(t, "printenv")
	if _, err := m.Spawn(c, "noagent", printenv, []string{"SSH_AUTH_SOCK"}, "", env, true); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := firstStdout(t, frames); got != "SSH_AUTH_SOCK=" {
		t.Errorf("child saw %q, want no SSH_AUTH_SOCK", got)
	}
	if *calls != 0 {
		t.Errorf("lookup ran %d times, want 0 when disabled", *calls)
	}
	waitExitFrame(t, frames)
}

// TestSpawnGitReadSkipsTheAgentStep: a gitRead spawn runs no agent step with a git
// (87375cc7 and 4c111e86, measured on Linux) and logs no agent line with none
// (measured on macOS). The next plain spawn runs it (measured with a login shell
// that exports none). Mutation: run AddAgentSocket for a gitRead spawn too.
func TestSpawnGitReadSkipsTheAgentStep(t *testing.T) {
	unsetForTest(t, "SSH_AUTH_SOCK")
	calls := stubShellAgentSocket(t, "/tmp/login-agent.sock")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	shellGit := exe
	old := GitReadShellPathGit
	GitReadShellPathGit = func() string { return shellGit }
	t.Cleanup(func() { GitReadShellPathGit = old })
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)

	stop := errors.New("stop after the env")
	var got []string
	_, err = m.SpawnGitRead(nil, "gs", nil, t.TempDir(), nil, func(env []string) ([]string, []string, func(), error) {
		got = env
		return nil, nil, nil, stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("SpawnGitRead error = %v, want the stop of the step", err)
	}
	if i := slices.IndexFunc(got, func(kv string) bool { return strings.HasPrefix(kv, "SSH_AUTH_SOCK=") }); i >= 0 {
		t.Errorf("the env given to the step holds %q, want no agent entry", got[i])
	}
	if *calls != 0 {
		t.Fatalf("lookup ran %d times after a gitRead spawn, want 0", *calls)
	}

	shellGit = ""
	if _, err = m.SpawnGitRead(nil, "gn", nil, t.TempDir(), nil, func([]string) ([]string, []string, func(), error) {
		t.Error("the step ran with no git")
		return nil, nil, nil, stop
	}); err == nil {
		t.Fatal("SpawnGitRead with no git succeeded")
	}
	if *calls != 0 {
		t.Fatalf("lookup ran %d times after a gitRead spawn with no git, want 0", *calls)
	}

	c, frames := pipeConn(t)
	printenv, env := helperCommand(t, "printenv")
	if _, err := m.Spawn(c, "plain", printenv, []string{"SSH_AUTH_SOCK"}, "", env, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := firstStdout(t, frames); got != "SSH_AUTH_SOCK=/tmp/login-agent.sock" {
		t.Errorf("plain child saw %q, want the login-shell socket", got)
	}
	if *calls != 1 {
		t.Errorf("lookup ran %d times after the plain spawn, want 1", *calls)
	}
	waitExitFrame(t, frames)
}
