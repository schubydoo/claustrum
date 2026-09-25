package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// stubShellAgentSocket replaces the login-shell lookup for one test and counts
// the calls, so a test can tell "not needed" from "needed, found nothing".
func stubShellAgentSocket(t *testing.T, sock string) *int {
	t.Helper()
	calls := 0
	old := shellAgentSocket
	shellAgentSocket = func() string { calls++; return sock }
	t.Cleanup(func() { shellAgentSocket = old })
	return &calls
}

func TestAddShellAgentSocket(t *testing.T) {
	base := []string{"HOME=/h", "PATH=/bin"}
	cases := []struct {
		name      string
		env       []string
		disabled  bool
		stub      string
		want      []string
		wantCalls int
	}{
		{"appended last when absent", base, false, "/tmp/a.sock",
			[]string{"HOME=/h", "PATH=/bin", "SSH_AUTH_SOCK=/tmp/a.sock"}, 1},
		{"nothing to hand on", base, false, "", base, 1},
		{"disabled", base, true, "/tmp/a.sock", base, 0},
		{"present wins", []string{"SSH_AUTH_SOCK=/mine"}, false, "/tmp/a.sock",
			[]string{"SSH_AUTH_SOCK=/mine"}, 0},
		{"present and empty still wins", []string{"SSH_AUTH_SOCK="}, false, "/tmp/a.sock",
			[]string{"SSH_AUTH_SOCK="}, 0},
		{"a longer name is not the key", []string{"SSH_AUTH_SOCKET=/x"}, false, "/tmp/a.sock",
			[]string{"SSH_AUTH_SOCKET=/x", "SSH_AUTH_SOCK=/tmp/a.sock"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubShellAgentSocket(t, tc.stub)
			env := append([]string(nil), tc.env...)
			got := addShellAgentSocket(env, tc.disabled)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("env = %q, want %q", got, tc.want)
			}
			if *calls != tc.wantCalls {
				t.Errorf("lookup ran %d times, want %d", *calls, tc.wantCalls)
			}
		})
	}
}

// unsetForTest removes key from the process env for one test and restores it.
func unsetForTest(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // registers the restore
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

func TestSpawnHandsChildTheShellAgentSocket(t *testing.T) {
	unsetForTest(t, "SSH_AUTH_SOCK")
	calls := stubShellAgentSocket(t, "/tmp/login-agent.sock")
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	c, frames := pipeConn(t)
	printenv, env := helperCommand(t, "printenv")
	if _, err := m.spawn(c, "agent", printenv, []string{"SSH_AUTH_SOCK"}, "", env, false); err != nil {
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
	t.Cleanup(m.killAll)
	c, frames := pipeConn(t)
	printenv, env := helperCommand(t, "printenv")
	if _, err := m.spawn(c, "noagent", printenv, []string{"SSH_AUTH_SOCK"}, "", env, true); err != nil {
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

// waitExitFrame reads frames until the exit frame, so the child's exit log line
// is written inside this test and not into the next test's log capture.
func waitExitFrame(t *testing.T, frames <-chan streamFrame) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-frames:
			if f.Stream == "exit" {
				return
			}
		case <-deadline:
			t.Fatal("no exit frame within deadline")
		}
	}
}

func TestSpawnParamsDisableShellAgentSocket(t *testing.T) {
	cases := []struct {
		params  string
		wantErr bool
		want    bool
	}{
		{`{"id":"a","command":"x","disableShellAgentSocket":true}`, false, true},
		{`{"id":"a","command":"x","disableShellAgentSocket":false}`, false, false},
		{`{"id":"a","command":"x","disableShellAgentSocket":null}`, false, false},
		{`{"id":"a","command":"x"}`, false, false},
		{`{"id":"a","command":"x","disableShellAgentSocket":"yes"}`, true, false},
		{`{"id":"a","command":"x","disableShellAgentSocket":1}`, true, false},
	}
	for _, tc := range cases {
		var p spawnParams
		bad := bindParams(&request{ID: 1, Params: json.RawMessage(tc.params)}, &p)
		if tc.wantErr {
			if bad == nil || bad.Error == nil || bad.Error.Code != codeInvalidParam || bad.Error.Message != "Invalid params" {
				t.Errorf("%s: got %+v, want -32602 Invalid params", tc.params, bad)
			}
			continue
		}
		if bad != nil {
			t.Errorf("%s: unexpected error %+v", tc.params, bad.Error)
		} else if p.DisableShellAgentSocket != tc.want {
			t.Errorf("%s: DisableShellAgentSocket = %v, want %v", tc.params, p.DisableShellAgentSocket, tc.want)
		}
	}
}

// TestProcessSpawnDisableShellAgentSocketOverRPC sends the param through the
// handler, so a handler that drops it fails here even though the unit tests of
// the pieces pass.
func TestProcessSpawnDisableShellAgentSocketOverRPC(t *testing.T) {
	unsetForTest(t, "SSH_AUTH_SOCK")
	stubShellAgentSocket(t, "/tmp/login-agent.sock")
	cl := dial(t, startSocketServer(t))
	exe, env := helperCommand(t, "printenv")
	for i, tc := range []struct {
		id      string
		disable bool
		want    string
	}{{"rpc-on", false, "SSH_AUTH_SOCK=/tmp/login-agent.sock"}, {"rpc-off", true, "SSH_AUTH_SOCK="}} {
		b, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": i + 1, "method": "process.spawn", "auth": testToken,
			"params": map[string]any{"id": tc.id, "command": exe, "args": []string{"SSH_AUTH_SOCK"},
				"env": env, "disableShellAgentSocket": tc.disable},
		})
		if err != nil {
			t.Fatal(err)
		}
		cl.call(string(b))
		if got := strings.TrimSpace(streamBytes(t, cl.waitExit(tc.id), "stdout")); got != tc.want {
			t.Errorf("%s: child saw %q, want %q", tc.id, got, tc.want)
		}
	}
}
