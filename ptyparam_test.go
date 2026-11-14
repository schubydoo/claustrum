package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSocketPlainSpawnPtyMember pins the pty member of a plain spawn (4c111e86 and
// c9125eb8). A bare value gets Invalid params and an object with no key gets the key
// text, and neither starts a child (measured on Linux, the bare value on Windows and
// macOS too). Absent and null run the child (measured on Linux). A bare value with no
// id, and an object with a valid key that runs with no terminal, are claustrum's own.
func TestSocketPlainSpawnPtyMember(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	const invalid = `{"code":-32602,"message":"Invalid params"}`
	const terminal = `{"code":-32602,"message":"A terminal needs a key of 32 bytes in hex"}`
	refused := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"true", map[string]any{"pty": true}, invalid},
		{"false", map[string]any{"pty": false}, invalid},
		{"one", map[string]any{"pty": 1}, invalid},
		{"yes", map[string]any{"pty": "yes"}, invalid},
		{"true no id", map[string]any{"pty": true, "id": ""}, invalid},
		{"empty object", map[string]any{"pty": map[string]any{}}, terminal},
		{"object no key", map[string]any{"pty": map[string]any{"cols": 80, "rows": 24}}, terminal},
	}
	var frames []json.RawMessage
	for i, c := range refused {
		got := string(cl.call(linkSpawnReq(t, i+1, "pr"+itoa(i), "echo", []string{"hi"}, c.extra)))
		want := `{"jsonrpc":"2.0","id":` + itoa(i+1) + `,"error":` + c.want + `}`
		if got != want {
			t.Errorf("%s: reply = %s\nwant %s", c.name, got, want)
		}
		frames = append(frames, json.RawMessage(got))
	}
	runs := []struct {
		name  string
		extra map[string]any
	}{
		{"absent", nil},
		{"null", map[string]any{"pty": nil}},
		{"object with key", map[string]any{"pty": map[string]any{"cols": 80, "rows": 24, "key": strings.Repeat("ab", 32)}}},
	}
	for i, c := range runs {
		id := len(refused) + i + 1
		pid := "ps" + itoa(i)
		got := string(cl.call(linkSpawnReq(t, id, pid, "echo", []string{"hi"}, c.extra)))
		if want := `{"jsonrpc":"2.0","id":` + itoa(id) + `,"result":{"success":true}}`; got != want {
			t.Errorf("%s: reply = %s\nwant %s", c.name, got, want)
			continue
		}
		if out := streamBytes(t, cl.waitExit(pid), "stdout"); out != "hi\n" {
			t.Errorf("%s: stdout = %q, want %q", c.name, out, "hi\n")
		}
		frames = append(frames, json.RawMessage(got))
	}
	// The children of the runs exited, so a refused spawn that had started a child
	// had its frames by now on this connection.
	for i, c := range refused {
		if f := cl.framesOf("pr" + itoa(i)); len(f) != 0 {
			t.Errorf("%s: %d stream frames, want none", c.name, len(f))
		}
	}
	assertGolden(t, "socket_process_pty_member.golden.json", encodeGolden(t, frames))
}
