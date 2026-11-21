//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Windows rows of the 89cb6289 captures, with the Windows
// recheck.

// TestSpawnLauncherRefusedOnWindows pins SW: any non-null launcher, an empty one
// too, is -32004 with the Windows text and the reason launcher (build 52d82f0a),
// before the cwd check, and the command does not run. c9125eb8 adds the windows_host
// fault before the reason, and a supplies spawn takes the shorter prefix (measured
// on Windows). A null launcher spawns the command.
func TestSpawnLauncherRefusedOnWindows(t *testing.T) {
	logs := captureLogBuf(t)
	s := newTestServer(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	nocwd := filepath.Join(t.TempDir(), "nope")
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32004,"message":"the managed launcher cannot be used: a launcher cannot be applied on a Windows host","data":{"launcherFault":"windows_host","reason":"launcher"}}}`
	supplies := `{"jsonrpc":"2.0","id":1,"error":{"code":-32004,"message":"the launcher cannot be used: a launcher cannot be applied on a Windows host","data":{"launcherFault":"windows_host","reason":"launcher"}}}`
	for row, params := range map[string]map[string]any{
		"cmd.exe":    {"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{`C:\Windows\System32\cmd.exe`}},
		"cmd.exe /c": {"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{`C:\Windows\System32\cmd.exe`, "/c"}},
		"empty":      {"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{}},
		"no cwd":     {"id": "p1", "command": exe, "args": []string{"hi"}, "cwd": nocwd, "launcher": []string{exe}},
		"check":      {"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{exe}, "launcherCheck": map[string]any{"sha256": []string{}}},
		"supplies":   {"id": "p1", "command": "", "launcher": []string{exe}, "launcherSuppliesCli": true, "launcherCheck": map[string]any{"path": exe}},
	} {
		want := want
		if row == "supplies" {
			want = supplies
		}
		b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken, "params": params})
		if err != nil {
			t.Fatal(err)
		}
		if got := dispatchRaw(t, s, string(b)); got != want {
			t.Errorf("%s:\n got %s\nwant %s", row, got, want)
		}
	}
	if !strings.Contains(logs.String(), "[process.Manager] Failed to start process p1: the managed launcher cannot be used: a launcher cannot be applied on a Windows host\n") {
		t.Errorf("no refusal line in:\n%s", logs.String())
	}

	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	echo, env := helperCommand(t, "echo")
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": "p3", "command": echo, "args": []string{"hi"}, "env": env, "launcher": nil}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(cl.call(string(b))); got != `{"jsonrpc":"2.0","id":1,"result":{"success":true}}` {
		t.Fatalf("(null launcher): %s", got)
	}
	if got := streamBytes(t, cl.waitExit("p3"), "stdout"); got != "hi\n" {
		t.Errorf("stdout %q, want %q", got, "hi\n")
	}
}

// managedWindowsFixture writes a settings file that names this test binary as the
// launcher, in the E2E variable's folder. Windows ignores both (measured).
func managedWindowsFixture(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	t.Setenv(managedSettingsDirEnv, d)
	b, err := json.Marshal(map[string]any{"processWrapper": exe})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, managedSettingsBase), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLauncherResolveNoneOnWindows pins a measured rule: Windows answers none, also for a
// settings file in the E2E variable's folder, with or without the gate.
func TestLauncherResolveNoneOnWindows(t *testing.T) {
	managedWindowsFixture(t)
	s := newTestServer(t)
	for _, gate := range []string{"", "1"} {
		t.Setenv(managedLauncherGateEnv, gate)
		for _, cli := range []string{"/opt/claude/cli", `C:\opt\claude\cli.exe`} {
			b, _ := json.Marshal(cli)
			got := dispatchRaw(t, s, authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":`+string(b)+`}}`))
			if want := `{"jsonrpc":"2.0","id":1,"result":{"status":"none"}}`; got != want {
				t.Errorf("gate %q, cliPath %s: %s, want %s", gate, cli, got, want)
			}
		}
	}
}

// TestLauncherResolveCheckOnWindows pins the Windows answer to a launcherCheck: an
// object is unusable with windows_host, also with a top-level path. The shapes on
// the launcher.resolve page are measured on Windows (4c111e86), and other member
// values are claustrum's own. It sends 5 refusals and wants one line. 27 refusals log one line (4c111e86, measured on Windows).
func TestLauncherResolveCheckOnWindows(t *testing.T) {
	managedWindowsFixture(t)
	logs := captureLogBuf(t)
	s := newTestServer(t)
	refused := `{"jsonrpc":"2.0","id":1,"result":{"status":"unusable","reason":"a launcher cannot be applied on a Windows host","fault":"windows_host","hostPolicySeen":false}}`
	for _, params := range []string{
		`{"cliPath":"x","launcherCheck":{}}`,
		`{"cliPath":"x","path":"C:\\l.exe","launcherCheck":{}}`,
		`{"cliPath":"x","launcherCheck":{"path":"C:\\l.exe"}}`,
		`{"cliPath":"x","launcherCheck":{"path":5}}`,
		`{"cliPath":"x","launcherCheck":{"path":"C:\\l.exe","orManagedFolder":"yes"}}`,
	} {
		if got := dispatchRaw(t, s, authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":`+params+`}`)); got != refused {
			t.Errorf("%s:\n got %s\nwant %s", params, got, refused)
		}
	}
	line := "[LauncherHandler] the launcher that the desktop names is refused: a launcher cannot be applied on a Windows host\n"
	if n := strings.Count(logs.String(), line); n != 1 {
		t.Errorf("%d refused lines, want 1:\n%s", n, logs.String())
	}
}

// TestLauncherResolveNoCheckRearmsOnWindows pins the rows of 4c111e86 on Windows: a
// refusal of a launcherCheck logs one line, the same again none. An answer with no
// launcherCheck makes the next equal refusal log again. A -32602 "launcherCheck is
// required with path" between them does not. The log counts are 1, 1, 1, 2, 2, 2
// (measured on Windows).
func TestLauncherResolveNoCheckRearmsOnWindows(t *testing.T) {
	managedWindowsFixture(t)
	logs := captureLogBuf(t)
	s := newTestServer(t)
	line := "[LauncherHandler] the launcher that the desktop names is refused: a launcher cannot be applied on a Windows host\n"
	check := `{"cliPath":"x","launcherCheck":{"path":"C:\\l.exe"}}`
	for i, row := range []struct {
		params string
		lines  int
	}{
		{check, 1},
		{check, 1},
		{`{"cliPath":"x"}`, 1},
		{check, 2},
		{`{"cliPath":"x","path":"C:\\l.exe"}`, 2},
		{check, 2},
	} {
		_ = dispatchRaw(t, s, authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":`+row.params+`}`))
		if n := strings.Count(logs.String(), line); n != row.lines {
			t.Errorf("request %d %s: %d refused lines, want %d:\n%s", i+1, row.params, n, row.lines, logs.String())
		}
	}
}
