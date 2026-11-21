package main

import (
	"encoding/json"
	"testing"
)

// sessionOffSpawn is a process.spawn request of the sleep helper with the member
// sessionFoldersOff set to the JSON text raw.
func sessionOffSpawn(t *testing.T, id int, procID, raw string) string {
	t.Helper()
	exe, env := helperCommand(t, "sleep")
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": procID, "command": exe, "args": []string{"30"}, "env": env,
			"sessionFoldersOff": json.RawMessage(raw)},
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	return string(b)
}

// With sessionOffTyped, a sessionFoldersOff of 1 gets Invalid params (4c111e86,
// measured on Linux, macOS and Windows), and so do "x", 0 and {} (measured on
// Linux and macOS). true, false and null start the child (measured on Linux and
// macOS). Without it, 1 starts the child too.
func TestSpawnSessionOffType(t *testing.T) {
	const invalid = `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`
	const ok = `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`
	for _, row := range []struct {
		typed bool
		raw   string
		want  string
	}{
		{true, `1`, invalid},
		{true, `"x"`, invalid},
		{true, `0`, invalid},
		{true, `{}`, invalid},
		{true, `"yes"`, invalid}, // claustrum's own, not measured
		{true, `true`, ok},
		{true, `false`, ok},
		{true, `null`, ok},
		{false, `1`, ok},
	} {
		t.Run(row.raw, func(t *testing.T) {
			prev := sessionOffTyped
			sessionOffTyped = row.typed
			t.Cleanup(func() { sessionOffTyped = prev })
			cl := dial(t, startSocketServer(t))
			if got := string(cl.call(sessionOffSpawn(t, 1, "SO", row.raw))); got != row.want {
				t.Fatalf("typed %v, sessionFoldersOff %s: reply %s, want %s", row.typed, row.raw, got, row.want)
			}
		})
	}
}

// sessionOffTyped holds on every system: 4c111e86 refuses a sessionFoldersOff of
// 1 on Linux, macOS and Windows (measured). Mutation: set sessionOffTyped to false
// in spawnsessionoff.go. The test then fails.
func TestSpawnSessionOffTypedOnEverySystem(t *testing.T) {
	if !sessionOffTyped {
		t.Fatal("sessionOffTyped is false, want true")
	}
}

// A gitRead spawn with a sessionFoldersOff of 1 gets Invalid params with
// sessionOffTyped (4c111e86, measured on Linux and macOS). Its place before the
// gitRead tests is claustrum's own, not measured.
func TestSpawnSessionOffTypeGitRead(t *testing.T) {
	prev := sessionOffTyped
	sessionOffTyped = true
	t.Cleanup(func() { sessionOffTyped = prev })
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": "GR", "command": "git", "args": []string{"status"}, "cwd": t.TempDir(),
			"gitRead": true, "sessionFoldersOff": 1},
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	const invalid = `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`
	cl := dial(t, startSocketServer(t))
	if got := string(cl.call(string(b))); got != invalid {
		t.Fatalf("reply %s, want %s", got, invalid)
	}
}
