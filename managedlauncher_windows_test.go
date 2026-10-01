//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Windows rows of the 89cb6289 VM captures (win SW, RW, IW, X1-X5 and
// the Windows recheck C2 row).

// TestSpawnLauncherRefusedOnWindows pins SW: any non-null launcher, an empty one
// too, is -32004 with the Windows text, before the cwd check, and the command does
// not run. A null launcher spawns the command.
func TestSpawnLauncherRefusedOnWindows(t *testing.T) {
	logs := captureLogBuf(t)
	s := newTestServer(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	nocwd := filepath.Join(t.TempDir(), "nope")
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32004,"message":"the managed launcher cannot be used: a launcher cannot be applied on a Windows host"}}`
	for row, params := range map[string]map[string]any{
		"SW1":  {"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{`C:\Windows\System32\cmd.exe`}},
		"SW1x": {"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{`C:\Windows\System32\cmd.exe`, "/c"}},
		"SW2":  {"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{}},
		"SW4":  {"id": "p1", "command": exe, "args": []string{"hi"}, "cwd": nocwd, "launcher": []string{exe}},
	} {
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
		t.Fatalf("SW3 (null launcher): %s", got)
	}
	if got := streamBytes(t, cl.waitExit("p3"), "stdout"); got != "hi\n" {
		t.Errorf("SW3 stdout %q, want %q", got, "hi\n")
	}
}

// managedWindowsFixture writes a settings file that names this test binary as the
// launcher, in the E2E variable's folder. Windows ignores both (X1, X2).
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

// TestLauncherResolveNoneOnWindows pins RES-30: Windows answers none, also for a
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

// TestInstallAndProbeOnWindowsWithGate pins INS-15 and PRB-7: with the gate,
// -install appends "launcherStatus":"none" last and runs the CLI directly, and
// -probe-cli shows no difference.
func TestInstallAndProbeOnWindowsWithGate(t *testing.T) {
	managedWindowsFixture(t)
	t.Setenv(managedLauncherGateEnv, "1")
	dir := filepath.Join(t.TempDir(), "cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(dir, "9.9.9")
	if err := os.WriteFile(cli, fakeCLI(t, 0), 0o755); err != nil {
		t.Fatal(err)
	}
	out := captureInstallOutput(t, installOpts{cliDir: dir, cliVersion: "9.9.9"})
	if !strings.HasSuffix(out, `"cliWasPresent":true,"launcherStatus":"none"}`+"\n") {
		t.Errorf("IW: facts %q, want launcherStatus none last", out)
	}
	var o, e bytes.Buffer
	runProbeCLI(&o, &e, cli)
	if o.String() != "" || e.String() != "" {
		t.Errorf("PRB-7: stdout %q stderr %q, want both empty", o.String(), e.String())
	}
}
