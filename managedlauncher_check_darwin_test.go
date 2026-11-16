//go:build darwin

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// macOSHostText is the macos_host reason of 4c111e86 (measured on macOS, a resolve with no path).
const macOSHostText = "This launcher can't be used on a macOS host: an access-control list there can let another account change it unseen. Use a Linux host."

// TestLauncherCheckRefusedOnMacOS pins the macos_host fault and order of the macOS rows of
// c9125eb8, with the text of 4c111e86: every spawn with a
// launcher and a launcherCheck gets macos_host, with the prefix of its shape, before
// any file test. Every launcher.resolve with a launcherCheck answers unusable with
// macos_host, also with no path and no settings file. The second of two equal
// refusals logs no line (4c111e86, measured on macOS).
func TestLauncherCheckRefusedOnMacOS(t *testing.T) {
	logs := captureLogBuf(t)
	tempManagedSettingsDir(t)
	s := newTestServer(t)
	wrap, exe := wrapLauncherFixture(t)
	nope := filepath.Join(t.TempDir(), "nope")
	frame := func(prefix string) string {
		return `{"jsonrpc":"2.0","id":1,"error":{"code":-32004,"message":"` + prefix + macOSHostText + `","data":{"launcherFault":"macos_host","reason":"launcher"}}}`
	}
	cases := []struct {
		row    string
		params map[string]any
		want   string
	}{
		{"supplies", map[string]any{"id": "p1", "command": "", "args": []string{"--x"}, "launcher": []string{wrap}, "launcherSuppliesCli": true, "launcherCheck": map[string]any{}}, frame("the launcher cannot be used: ")},
		{"command", map[string]any{"id": "p1", "command": exe, "launcher": []string{wrap}, "launcherCheck": map[string]any{"sha256": []string{"00"}}}, frame("the managed launcher cannot be used: ")},
		{"missing launcher", map[string]any{"id": "p1", "command": exe, "launcher": []string{nope}, "launcherCheck": map[string]any{}}, frame("the managed launcher cannot be used: ")},
	}
	for _, c := range cases {
		if got := dispatchRaw(t, s, launchedSpawnReq(t, c.params)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.row, got, c.want)
		}
	}
	want := `{"jsonrpc":"2.0","id":1,"result":{"status":"unusable","reason":"` + macOSHostText + `","fault":"macos_host","hostPolicySeen":false}}`
	for _, params := range []string{`{"cliPath":"/x/cli","launcherCheck":{}}`, `{"cliPath":"/x/cli","path":` + jsonText(t, wrap) + `,"launcherCheck":{"ownerUid":501}}`} {
		if got := dispatchRaw(t, s, authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":`+params+`}`)); got != want {
			t.Errorf("resolve %s:\n got %s\nwant %s", params, got, want)
		}
	}
	line := "[LauncherHandler] the launcher that the desktop names is refused: " + macOSHostText + "\n"
	if n := strings.Count(logs.String(), line); n != 1 {
		t.Errorf("%d refusal lines, want 1 (one per run of equal refusals):\n%s", n, logs.String())
	}
}

// TestLauncherCheckLogOnceOnMacOS pins the macOS rows of 4c111e86: a run of equal
// refusals of a launcherCheck logs one line, and a -32602 reply in the run does not
// end it. A resolve with no launcherCheck ends it, a usable answer and a managed
// refusal alike.
func TestLauncherCheckLogOnceOnMacOS(t *testing.T) {
	logs := captureLogBuf(t)
	dir := tempManagedSettingsDir(t)
	base := filepath.Join(dir, managedSettingsBase)
	w := mkExec(t, filepath.Join(dir, "bin", "wrap"))
	s := newTestServer(t)
	line := "[LauncherHandler] the launcher that the desktop names is refused: " + macOSHostText + "\n"
	resolve := func(params string) {
		t.Helper()
		dispatchRaw(t, s, authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":`+params+`}`))
	}
	check := func() { t.Helper(); resolve(`{"cliPath":"/x/cli","launcherCheck":{}}`) }
	steps := []struct {
		row   string
		ask   func()
		lines int
	}{
		{"first refusal", check, 1},
		{"equal refusal", check, 1},
		{"-32602", func() { resolve(`{"cliPath":"/x/cli","launcherCheck":{"ownerUid":"501"}}`) }, 1},
		{"refusal after -32602", check, 1},
		{"usable answer", func() { writeFileMode(t, base, []byte(pwSettings(t, w)), 0o644); resolve(`{"cliPath":"/x/cli"}`) }, 1},
		{"refusal after usable", check, 2},
		{"managed refusal", func() { writeFileMode(t, base, []byte(pwSettings(t, "wrap")), 0o644); resolve(`{"cliPath":"/x/cli"}`) }, 2},
		{"refusal after managed refusal", check, 3},
		{"equal refusal again", check, 3},
	}
	for _, st := range steps {
		st.ask()
		if n := strings.Count(logs.String(), line); n != st.lines {
			t.Fatalf("after %s: %d refusal lines, want %d:\n%s", st.row, n, st.lines, logs.String())
		}
	}
}
