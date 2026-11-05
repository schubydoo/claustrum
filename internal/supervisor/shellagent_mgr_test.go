package supervisor

import (
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
