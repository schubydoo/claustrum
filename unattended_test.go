package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// unattendedReq is a process.spawn request with the members of params.
func unattendedReq(t *testing.T, id int, params map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	return string(b)
}

// TestSocketUnattendedSecondsRefusals pins the replies of 87375cc7 to the member
// unattendedSeconds (each row measured on Linux, the plain rows also on Windows, and
// on macOS -1, cliTempRootEnd, gitRead, a ccd-cli with a stream-json arg and a
// text): the refusal text with the reason other, also before the linkBound text
// with a launcher, Invalid params for a value that is no integer, the keyless pty
// text, and a start for 0, 600, 10^12 and a stream-json start with
// --no-session-persistence. A refused spawn starts no child, so its command need
// not exist.
func TestSocketUnattendedSecondsRefusals(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cwd := t.TempDir()
	exe, env := helperCommand(t, "echo")
	const refused = `{"code":-32602,"message":"unattendedSeconds refused: it is negative, or came with pty, linkBound, gitRead or cliTempRootEnd, or for a Claude Code that was not started with --no-session-persistence","data":{"reason":"other"}}`
	const invalid = `{"code":-32602,"message":"Invalid params"}`
	const terminal = `{"code":-32602,"message":"A terminal needs a key of 32 bytes in hex"}`
	const started = `{"success":true}`
	key := strings.Repeat("0", 64)
	plain := func(extra map[string]any) map[string]any {
		p := map[string]any{"command": exe, "args": []string{"x"}, "env": env, "cwd": cwd}
		for k, v := range extra {
			p[k] = v
		}
		return p
	}
	cases := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"negative", plain(map[string]any{"unattendedSeconds": -1}), refused},
		{"linkBound object", plain(map[string]any{"unattendedSeconds": 600, "linkBound": map[string]any{"linkId": "0123456789abcdef"}}), refused},
		{"gitRead", map[string]any{"command": "git", "args": []string{"rev-parse", "--show-toplevel"}, "cwd": cwd, "gitRead": true, "unattendedSeconds": 600}, refused},
		{"cliTempRootEnd", plain(map[string]any{"unattendedSeconds": 600, "cliTempRootEnd": true}), refused},
		{"pty with key", plain(map[string]any{"unattendedSeconds": 600, "pty": map[string]any{"cols": 80, "rows": 24, "key": key}}), refused},
		{"ccd-cli", map[string]any{"command": "/no/such/folder/ccd-cli", "args": []string{"-p", "x"}, "cwd": cwd, "unattendedSeconds": 600}, refused},
		{"folder named ccd-cli", map[string]any{"command": "/no/such/ccd-cli/2.0.0", "args": []string{"-p", "x"}, "cwd": cwd, "unattendedSeconds": 600}, refused},
		{"before linkBound with a launcher", plain(map[string]any{"unattendedSeconds": -1, "linkBound": map[string]any{"linkId": "0123456789abcdef"}, "launcher": []string{exe}}), refused},
		{"output-format=stream-json", plain(map[string]any{"args": []string{"--output-format=stream-json"}, "unattendedSeconds": 600}), refused},
		{"stream-json", plain(map[string]any{"args": []string{"--output-format", "stream-json", "-p", "x"}, "unattendedSeconds": 600}), refused},
		{"text", plain(map[string]any{"unattendedSeconds": "600"}), invalid},
		{"float", plain(map[string]any{"unattendedSeconds": 1.5}), invalid},
		{"null", plain(map[string]any{"unattendedSeconds": nil}), invalid},
		{"pty true", plain(map[string]any{"unattendedSeconds": 600, "pty": true}), invalid},
		{"linkBound true", plain(map[string]any{"unattendedSeconds": 600, "linkBound": true}), invalid},
		{"pty object no key", plain(map[string]any{"unattendedSeconds": 600, "pty": map[string]any{"cols": 80, "rows": 24}}), terminal},
		{"zero", plain(map[string]any{"unattendedSeconds": 0}), started},
		{"600", plain(map[string]any{"unattendedSeconds": 600}), started},
		{"10^12", plain(map[string]any{"unattendedSeconds": 1000000000000}), started},
		{"stream-json with the flag", plain(map[string]any{"args": []string{"--output-format", "stream-json", "--no-session-persistence"}, "unattendedSeconds": 600}), started},
	}
	for i, c := range cases {
		procID := "u" + itoa(i+1)
		c.params["id"] = procID
		got := string(cl.call(unattendedReq(t, i+1, c.params)))
		member := `"error":` + c.want
		if c.want == started {
			member = `"result":` + c.want
		}
		want := `{"jsonrpc":"2.0","id":` + itoa(i+1) + `,` + member + `}`
		if got != want {
			t.Errorf("%s: reply = %s\nwant %s", c.name, got, want)
		}
		if c.want == started {
			cl.waitExit(procID)
		}
	}
}

// TestUnattendedSecondsLogLine pins the failed-start line of the refusal
// (87375cc7, measured on Linux), and no line for Invalid params.
func TestUnattendedSecondsLogLine(t *testing.T) {
	logs := captureLogBuf(t)
	cwd := t.TempDir()
	spawnErrorOf(t, map[string]any{"id": "un-l1", "command": "/no/such/plain", "cwd": cwd, "unattendedSeconds": -1})
	spawnErrorOf(t, map[string]any{"id": "un-l2", "command": "/no/such/plain", "cwd": cwd, "unattendedSeconds": "60"})
	line := "[process.Manager] Failed to start process un-l1: unattendedSeconds refused: it is negative, or came with pty, linkBound, gitRead or cliTempRootEnd, or for a Claude Code that was not started with --no-session-persistence"
	if !strings.Contains(logs.String(), line) {
		t.Errorf("log lacks %q:\n%s", line, logs.String())
	}
	if strings.Contains(logs.String(), "process un-l2") {
		t.Errorf("log names un-l2, want no line for Invalid params:\n%s", logs.String())
	}
}

// TestUnattendedRefusedRules pins the rules of unattendedRefused that need a
// command the socket test cannot start (87375cc7, measured on Linux): a ccd-cli
// with --no-session-persistence passes, 0 passes with gitRead and for a ccd-cli,
// and a bare git with a stream-json arg passes. A launcher spawn counts as a Claude
// Code start.
func TestUnattendedRefusedRules(t *testing.T) {
	cases := []struct {
		name string
		p    spawnParams
		n    int64
		want bool
	}{
		{"ccd-cli with the flag", spawnParams{Command: "/x/ccd-cli", Args: []string{"-p", "x", "--no-session-persistence"}}, 600, false},
		{"ccd-cli without the flag", spawnParams{Command: "/x/ccd-cli", Args: []string{"-p", "x"}}, 600, true},
		{"zero with gitRead", spawnParams{Command: "git", GitRead: true}, 0, false},
		{"zero for a ccd-cli", spawnParams{Command: "/x/ccd-cli"}, 0, false},
		{"bare git with stream-json", spawnParams{Command: "git", Args: []string{"--version", "stream-json"}}, 600, false},
		{"launcher", spawnParams{Command: "/x/plain", Launcher: &[]string{"/x/l"}}, 600, true},
		{"plain", spawnParams{Command: "/x/plain"}, 600, false},
		{"negative", spawnParams{Command: "/x/plain"}, -1, true},
	}
	for _, c := range cases {
		if got := unattendedRefused(&c.p, c.n); got != c.want {
			t.Errorf("%s: unattendedRefused = %v, want %v", c.name, got, c.want)
		}
	}
}
