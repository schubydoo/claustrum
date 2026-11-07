//go:build darwin

package main

import (
	"path/filepath"
	"testing"
)

// macOSHostText is the macos_host reason of c9125eb8 (measured on macOS).
const macOSHostText = "sshHostLauncher cannot be used with a macOS host: an access-control list there can let another account change the launcher without this file check seeing it."

// TestLauncherCheckRefusedOnMacOS pins the macOS rows of c9125eb8: every spawn with a
// launcher and a launcherCheck gets macos_host, with the prefix of its shape, before
// any file test. Every launcher.resolve with a launcherCheck answers unusable with
// macos_host, also with no path and no settings file.
func TestLauncherCheckRefusedOnMacOS(t *testing.T) {
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
}
