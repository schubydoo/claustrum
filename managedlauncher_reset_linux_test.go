//go:build linux

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLauncherCheckResetCallSite drives the refused line of a launcherCheck through
// handleLauncher. On Linux, with the flag as built, a usable resolve with no
// launcherCheck keeps the run (claustrum's own). With the macOS arm it ends the run, and a -32602 reply to a
// launcherCheck does not (4c111e86, measured on macOS).
func TestLauncherCheckResetCallSite(t *testing.T) {
	line := "[LauncherHandler] the launcher that the desktop names is refused: " + managedNotAbsolute + "\n"
	for _, c := range []struct {
		resets bool
		want   []int
	}{
		{false, []int{1, 1, 1, 1, 1, 1}},
		{true, []int{1, 1, 1, 1, 1, 2}},
	} {
		t.Run(map[bool]string{false: "linux", true: "macos arm"}[c.resets], func(t *testing.T) {
			logs := captureLogBuf(t)
			dir := tempManagedSettingsDir(t)
			w := mkExec(t, filepath.Join(dir, "bin", "wrap"))
			writeFileMode(t, filepath.Join(dir, managedSettingsBase), []byte(pwSettings(t, w)), 0o644)
			if c.resets {
				answerResetsCheck = true
				t.Cleanup(func() { answerResetsCheck = launcherAnswerResetsCheck })
			}
			s := newTestServer(t)
			resolve := func(params string) {
				t.Helper()
				dispatchRaw(t, s, authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":`+params+`}`))
			}
			check := `{"cliPath":"/x/cli","launcherCheck":{}}`
			for i, params := range []string{check, check, `{"cliPath":"/x/cli","launcherCheck":{"ownerUid":"1"}}`, check, `{"cliPath":"/x/cli"}`, check} {
				resolve(params)
				if n := strings.Count(logs.String(), line); n != c.want[i] {
					t.Fatalf("resets %v, after ask %d: %d refusal lines, want %d:\n%s", c.resets, i+1, n, c.want[i], logs.String())
				}
			}
		})
	}
}
