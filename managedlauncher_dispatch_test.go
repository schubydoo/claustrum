package main

import (
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/supervisor"
)

// TestLauncherResolveParams pins the params checks of launcher.resolve (measured,
// also in the Linux and Windows recheck). None of these reads a settings file, so
// they hold on every OS.
func TestLauncherResolveParams(t *testing.T) {
	s := newTestServer(t)
	cases := []struct{ line, want string }{
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve"}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"cliPath is required"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":""}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"cliPath is required"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":null}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"cliPath is required"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":null}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"cliPath is required"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"path":"/x"}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"cliPath is required"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":5}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":[]}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.bogus","params":{}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Unknown method: launcher.bogus"}}`},
		{`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":"/x"}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"Unauthorized: invalid or missing auth token"}}`},
	}
	for _, c := range cases {
		if got := dispatchRaw(t, s, c.line); got != c.want {
			t.Errorf("%s\n got %s\nwant %s", c.line, got, c.want)
		}
	}
}

// TestSpawnLauncherDecodeErrors pins a measured rule: a non-array launcher or a non-string
// element is -32602 Invalid params, with no [process.Manager] failure line.
func TestSpawnLauncherDecodeErrors(t *testing.T) {
	logs := captureLogBuf(t)
	s := newTestServer(t)
	for _, l := range []string{`"x"`, `[1]`} {
		line := authed(`{"jsonrpc":"2.0","id":1,"method":"process.spawn","params":{"id":"p1","command":"/x/echo","args":["hi"],"launcher":` + l + `}}`)
		if got, want := dispatchRaw(t, s, line), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`; got != want {
			t.Errorf("launcher %s: %s, want %s", l, got, want)
		}
	}
	// Only a spawn failure line counts. An earlier test's child still logs its
	// exit into this buffer at times.
	if strings.Contains(logs.String(), "[process.Manager] Failed to start process p1") {
		t.Errorf("a decode error must write no [process.Manager] line:\n%s", logs.String())
	}
}

// TestManagedSpawnTexts pins the texts of the spawn checks of a launcher, byte for
// byte, as the captures show them. TestManagedLauncherTexts in internal/install
// pins the other texts of the managed launcher.
func TestManagedSpawnTexts(t *testing.T) {
	texts := map[string]string{
		supervisor.ManagedSpawnRelative:  "command must be an absolute path when a launcher is given",
		supervisor.ManagedSpawnCannotUse: "the managed launcher cannot be used: ",
		supervisor.ManagedNoInterpreter:  "its interpreter was not found (the program on its #! line, or the loader of an ELF binary; a script saved with Windows CRLF line endings fails this way)",
	}
	for got, want := range texts {
		if got != want {
			t.Errorf("text = %q, want %q", got, want)
		}
	}
}

// The names of internal/install that the tests of this package use, copied.
// TestManagedLauncherTexts in internal/install pins the same values.
const (
	managedLauncherGateEnv = "CLAUDE_SSH_MANAGED_LAUNCHER"
	managedSettingsDirEnv  = "CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR"
	managedSettingsBase    = "managed-settings.json"
)
