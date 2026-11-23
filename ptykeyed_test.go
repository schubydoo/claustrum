package main

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	keyedPtyFrame = `{"code":-32602,"message":"this host's helper cannot open a terminal","data":{"reason":"other"}}`
	badParamFrame = `{"code":-32602,"message":"Invalid params"}`
	badKeyFrame   = `{"code":-32602,"message":"A terminal needs a key of 32 bytes in hex"}`
)

// keyedPtyOn turns the Windows rule on for one test, on every system.
func keyedPtyOn(t *testing.T) {
	t.Helper()
	prev := keyedPtyRefused
	keyedPtyRefused = true
	t.Cleanup(func() { keyedPtyRefused = prev })
}

// ptyWith is a valid pty object with the members of extra. A nil value drops a member.
func ptyWith(extra map[string]any) map[string]any {
	o := map[string]any{"cols": 80, "rows": 24, "key": strings.Repeat("ab", 32)}
	for k, v := range extra {
		if v == nil {
			delete(o, k)
			continue
		}
		o[k] = v
	}
	return o
}

// TestSpawnTempRootAskMarksAPty: only a pty object marks the spawn, so that the
// shutdown drain kill skips it (87375cc7, measured on Linux and macOS).
func TestSpawnTempRootAskMarksAPty(t *testing.T) {
	if !spawnTempRootAsk(&spawnParams{Pty: json.RawMessage(`{"cols":80}`)}).Pty {
		t.Error("a pty object did not mark the spawn")
	}
	for _, v := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("true")} {
		if spawnTempRootAsk(&spawnParams{Pty: v}).Pty {
			t.Errorf("the pty member %q marked the spawn", v)
		}
	}
}

// TestKeyedPtyRefusedOnWindowsOnly pins the system of the rule. 4c111e86 and 87375cc7
// refuse a keyed pty on Windows (measured). Linux and macOS keep the plain spawn of
// claustrum (claustrum's own).
func TestKeyedPtyRefusedOnWindowsOnly(t *testing.T) {
	if want := runtime.GOOS == "windows"; keyedPtyRefused != want {
		t.Fatalf("keyedPtyRefused = %v on %s, want %v", keyedPtyRefused, runtime.GOOS, want)
	}
}

// TestSocketKeyedPtyOrder pins the order of the refusal (87375cc7, measured on
// Windows). A bad member, id, command, key, gitRead and unattendedSeconds reply comes
// first. The refusal wins over the command lookup, the cwd test, the launcher refusal
// and linkBound with a launcher.
func TestSocketKeyedPtyOrder(t *testing.T) {
	keyedPtyOn(t)
	cl := dial(t, startSocketServer(t))
	exe, env := helperCommand(t, "echo")
	gone := filepath.Join(t.TempDir(), "gone")
	const unattended = `{"code":-32602,"message":"unattendedSeconds refused: it is negative, or came with pty, linkBound, gitRead or cliTempRootEnd, or for a Claude Code that was not started with --no-session-persistence","data":{"reason":"other"}}`
	const combine = `{"code":-32602,"message":"gitRead needs the command \"git\" and a cwd, and cannot be combined with env, launcher, pty, linkBound, cliTempRoot, cliTempRootEnd or sessionFoldersOff","data":{"reason":"other"}}`
	const noID = `{"code":-32602,"message":"Process ID is required"}`
	const noCommand = `{"code":-32602,"message":"Command is required"}`
	cases := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"keyed", nil, keyedPtyFrame},
		{"uppercase key", map[string]any{"pty": ptyWith(map[string]any{"key": strings.Repeat("AB", 32)})}, keyedPtyFrame},
		{"no cols", map[string]any{"pty": ptyWith(map[string]any{"cols": nil})}, keyedPtyFrame},
		{"no rows", map[string]any{"pty": ptyWith(map[string]any{"rows": nil})}, keyedPtyFrame},
		{"no size", map[string]any{"pty": ptyWith(map[string]any{"cols": nil, "rows": nil})}, keyedPtyFrame},
		{"cols 0", map[string]any{"pty": ptyWith(map[string]any{"cols": 0})}, keyedPtyFrame},
		{"rows 0", map[string]any{"pty": ptyWith(map[string]any{"rows": 0})}, keyedPtyFrame},
		{"missing command", map[string]any{"command": gone}, keyedPtyFrame},
		{"missing cwd", map[string]any{"cwd": gone}, keyedPtyFrame},
		{"empty launcher", map[string]any{"launcher": []string{}}, keyedPtyFrame},
		{"relative launcher", map[string]any{"launcher": []string{"rel.exe"}}, keyedPtyFrame},
		{"missing launcher", map[string]any{"launcher": []string{gone}}, keyedPtyFrame},
		{"linkBound", map[string]any{"linkBound": map[string]any{}}, keyedPtyFrame},
		{"linkBound with a launcher", map[string]any{"linkBound": map[string]any{}, "launcher": []string{exe}}, keyedPtyFrame},
		{"cliTempRoot", map[string]any{"cliTempRoot": true}, keyedPtyFrame},
		{"cliTempRootEnd", map[string]any{"cliTempRootEnd": true}, keyedPtyFrame},
		{"cols negative", map[string]any{"pty": ptyWith(map[string]any{"cols": -5})}, badParamFrame},
		{"rows negative", map[string]any{"pty": ptyWith(map[string]any{"rows": -5})}, badParamFrame},
		{"cols text", map[string]any{"pty": ptyWith(map[string]any{"cols": "wide"})}, badParamFrame},
		{"rows text", map[string]any{"pty": ptyWith(map[string]any{"rows": "tall"})}, badParamFrame},
		{"cols 10^12", map[string]any{"pty": ptyWith(map[string]any{"cols": int64(1000000000000)})}, badParamFrame},
		{"rows 10^12", map[string]any{"pty": ptyWith(map[string]any{"rows": int64(1000000000000)})}, badParamFrame},
		{"cols 80.5", map[string]any{"pty": ptyWith(map[string]any{"cols": 80.5})}, badParamFrame},
		{"key number", map[string]any{"pty": ptyWith(map[string]any{"key": 12345})}, badParamFrame},
		{"launcherCheck true", map[string]any{"launcherCheck": true}, badParamFrame},
		{"sessionFoldersOff 1", map[string]any{"sessionFoldersOff": 1}, badParamFrame},
		{"empty id", map[string]any{"id": ""}, noID},
		{"empty command", map[string]any{"command": ""}, noCommand},
		{"key not hex", map[string]any{"pty": ptyWith(map[string]any{"key": strings.Repeat("zz", 32)})}, badKeyFrame},
		{"key of 63", map[string]any{"pty": ptyWith(map[string]any{"key": strings.Repeat("a", 63)})}, badKeyFrame},
		{"key of 65", map[string]any{"pty": ptyWith(map[string]any{"key": strings.Repeat("a", 65)})}, badKeyFrame},
		{"empty key", map[string]any{"pty": ptyWith(map[string]any{"key": ""})}, badKeyFrame},
		{"unattended -1", map[string]any{"unattendedSeconds": -1}, unattended},
		{"unattended 60", map[string]any{"unattendedSeconds": 60}, unattended},
		{"gitRead", map[string]any{"gitRead": true, "command": "git", "args": []string{"--version"}, "cwd": t.TempDir(), "env": map[string]string{}}, combine},
	}
	for i, c := range cases {
		params := map[string]any{"id": "o" + itoa(i), "command": exe, "args": []string{"hi"}, "env": env, "pty": ptyWith(nil)}
		for k, v := range c.extra {
			params[k] = v
		}
		got := string(cl.call(unattendedReq(t, i+1, params)))
		if want := `{"jsonrpc":"2.0","id":` + itoa(i+1) + `,"error":` + c.want + `}`; got != want {
			t.Errorf("%s: reply = %s\nwant %s", c.name, got, want)
		}
	}
	// ptyWith drops a nil member, so the request of a null key is raw.
	raw := `{"jsonrpc":"2.0","id":99,"method":"process.spawn","auth":"` + testToken + `","params":{"id":"onull","command":"x","pty":{"key":null}}}`
	got := string(cl.call(raw))
	if want := `{"jsonrpc":"2.0","id":99,"error":` + badKeyFrame + `}`; got != want {
		t.Errorf("null key: reply = %s\nwant %s", got, want)
	}
	for i := range cases {
		if f := cl.framesOf("o" + itoa(i)); len(f) != 0 {
			t.Errorf("%s: %d stream frames, want none", cases[i].name, len(f))
		}
	}
}

// TestSocketKeyedPtyLiveID: a keyed spawn of a live id gets the refusal (87375cc7,
// measured on Windows). That the live process still takes stdin is claustrum's own.
func TestSocketKeyedPtyLiveID(t *testing.T) {
	keyedPtyOn(t)
	cl := dial(t, startSocketServer(t))
	got := string(cl.call(linkSpawnReq(t, 1, "live", "cat", nil, nil)))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
		t.Fatalf("plain spawn: %s", got)
	}
	got = string(cl.call(linkSpawnReq(t, 2, "live", "cat", nil, map[string]any{"pty": ptyWith(nil)})))
	if want := `{"jsonrpc":"2.0","id":2,"error":` + keyedPtyFrame + `}`; got != want {
		t.Fatalf("keyed spawn: reply = %s\nwant %s", got, want)
	}
	got = string(cl.call(authed(`{"jsonrpc":"2.0","id":3,"method":"process.stdin","params":{"id":"live","data":"aGkK"}}`)))
	if !strings.Contains(got, `"applied":3`) {
		t.Fatalf("stdin to the live process: %s", got)
	}
	cl.call(authed(`{"jsonrpc":"2.0","id":4,"method":"process.kill","params":{"id":"live"}}`))
	cl.waitExit("live")
}

// TestSocketKeyedPtyLeavesNoID: a refused id stays free, process.stdin finds no
// process, and a kill, killAndWait or reattach of it gives it up, so that the next
// spawn gets the given-up refusal (4c111e86 and 87375cc7, measured on Windows).
func TestSocketKeyedPtyLeavesNoID(t *testing.T) {
	keyedPtyOn(t)
	logs := captureLogBuf(t)
	cl := dial(t, startSocketServer(t))
	const givenUp = `{"code":-32602,"message":"this id was asked after, or asked to be ended, while no process had it: take a fresh one","data":{"reason":"other"}}`
	next := 0
	req := func(id string, extra map[string]any) string {
		next++
		return string(cl.call(linkSpawnReq(t, next, id, "echo", []string{"hi"}, extra)))
	}
	refuse := func(id string) {
		t.Helper()
		got := req(id, map[string]any{"pty": ptyWith(nil)})
		if want := `{"jsonrpc":"2.0","id":` + itoa(next) + `,"error":` + keyedPtyFrame + `}`; got != want {
			t.Fatalf("%s: reply = %s\nwant %s", id, got, want)
		}
	}

	starts := func(id string) {
		t.Helper()
		got := req(id, nil)
		if want := `{"jsonrpc":"2.0","id":` + itoa(next) + `,"result":{"success":true}}`; got != want {
			t.Fatalf("plain spawn of %s: reply = %s\nwant %s", id, got, want)
		}
		cl.waitExit(id)
	}

	refuse("free")
	starts("free")

	refuse("nostdin")
	next++
	got := string(cl.call(authed(`{"jsonrpc":"2.0","id":` + itoa(next) + `,"method":"process.stdin","params":{"id":"nostdin","data":"aGk="}}`)))
	if want := `{"jsonrpc":"2.0","id":` + itoa(next) + `,"error":{"code":-32602,"message":"Process not found"}}`; got != want {
		t.Fatalf("stdin: reply = %s\nwant %s", got, want)
	}
	starts("nostdin")

	for _, method := range []string{"process.kill", "process.killAndWait", "process.reattach"} {
		id := "up-" + strings.TrimPrefix(method, "process.")
		refuse(id)
		next++
		cl.call(authed(`{"jsonrpc":"2.0","id":` + itoa(next) + `,"method":"` + method + `","params":{"id":"` + id + `"}}`))
		got := req(id, nil)
		if want := `{"jsonrpc":"2.0","id":` + itoa(next) + `,"error":` + givenUp + `}`; got != want {
			t.Errorf("%s: spawn after it: reply = %s\nwant %s", method, got, want)
		}
	}

	var lines int
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "[process.Manager] Failed to start process free: this host's helper cannot open a terminal") {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("%d refusal lines for the id free, want 1:\n%s", lines, logs.String())
	}
}

// TestPtyMembersBadNeedsTheRule: with the rule off, a bad size passes the decode,
// as on Linux and macOS today.
func TestPtyMembersBadNeedsTheRule(t *testing.T) {
	prev := keyedPtyRefused
	keyedPtyRefused = false
	t.Cleanup(func() { keyedPtyRefused = prev })
	if ptyMembersBad(json.RawMessage(`{"cols":-5}`)) {
		t.Fatal("ptyMembersBad = true with the rule off")
	}
}
