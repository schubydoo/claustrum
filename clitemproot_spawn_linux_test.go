package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func spawnLine(t *testing.T, id, command string, env map[string]string, flag bool) string {
	t.Helper()
	params := map[string]any{"id": id, "command": command, "env": env}
	if flag {
		params["cliTempRoot"] = true
	}
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tempRootsLive() int {
	tempRoots.mu.Lock()
	defer tempRoots.mu.Unlock()
	return tempRoots.live
}

// waitTempRootsLive polls the live count until it is n, with a limit.
func waitTempRootsLive(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for tempRootsLive() != n {
		if time.Now().After(deadline) {
			t.Fatalf("live count %d, want %d", tempRootsLive(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The frame of the spawn is the measured one, the child gets the folder, and the
// exit of the child gives the folder back.
func TestSpawnCLITempRootFrame(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	old := newTempRootName
	newTempRootName = func() string { return "ccd-a5247ffbe09b" }
	t.Cleanup(func() { newTempRootName = old })
	x, _ := tempRootDir(t, true)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	exe, env := helperCommand(t, "environ")
	env["TMPDIR"] = x
	raw := cl.call(spawnLine(t, "f1b", exe, env, true))
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"cliTempRoot":{"refused":"a link","name":"ccd-a5247ffbe09b"}}}`
	if string(raw) != want || len(want)-1 != 110 {
		t.Fatalf("reply %s, want %s", raw, want)
	}
	lines := strings.Split(streamBytes(t, cl.waitExit("f1b"), "stdout"), "\n")
	if !slices.Contains(lines, "CLAUDE_CODE_TMPDIR="+filepath.Join(x, "ccd-a5247ffbe09b")) || !slices.Contains(lines, "TMPDIR="+x) {
		t.Errorf("child env %q, want the folder and TMPDIR", lines)
	}
	waitTempRootsLive(t, 0)
}

// Without the flag the reply and the env stay as before.
func TestSpawnWithoutCLITempRootAddsNothing(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, _ := tempRootDir(t, true)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	exe, env := helperCommand(t, "environ")
	env["TMPDIR"] = x
	if raw := cl.call(spawnLine(t, "p", exe, env, false)); string(raw) != `{"jsonrpc":"2.0","id":1,"result":{"success":true}}` {
		t.Fatalf("reply %s", raw)
	}
	out := streamBytes(t, cl.waitExit("p"), "stdout")
	if strings.Contains(out, "CLAUDE_CODE_TMPDIR=") || len(ccdFolders(t, x)) != 0 {
		t.Errorf("a folder without the flag: %q", out)
	}
}

// A spawn that does not start gives the folder back.
func TestSpawnCLITempRootStartFailureGivesItBack(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, _ := tempRootDir(t, true)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	raw := cl.call(spawnLine(t, "gone", filepath.Join(x, "no-such-program"), map[string]string{"TMPDIR": x}, true))
	if !strings.Contains(string(raw), `"error"`) {
		t.Fatalf("reply %s, want an error", raw)
	}
	waitTempRootsLive(t, 0)
}
