//go:build windows

package install

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Windows rows of the 89cb6289 captures that need no daemon.

// noManagedPolicy is a no-op on Windows: there is no settings folder, so no
// policy applies (managedlauncher_windows.go).
func noManagedPolicy(t *testing.T) { t.Helper() }

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

// TestInstallAndProbeOnWindowsWithGate pins measured rules: with the gate,
// -install appends "launcherStatus":"none" last and runs the CLI directly, and
// -probe-cli shows no difference.
func TestInstallAndProbeOnWindowsWithGate(t *testing.T) {
	managedWindowsFixture(t)
	t.Setenv(managedLauncherGateEnv, "1")
	dir := filepath.Join(t.TempDir(), "cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cli := installCLIPath(dir, "9.9.9")
	if err := os.WriteFile(cli, fakeCLI(t, 0), 0o755); err != nil {
		t.Fatal(err)
	}
	out := captureInstallOutput(t, installOpts{cliDir: dir, cliVersion: "9.9.9"})
	if !strings.HasSuffix(out, `"cliWasPresent":true,"launcherStatus":"none"}`+"\n") {
		t.Errorf("IW: facts %q, want launcherStatus none last", out)
	}
	var o, e bytes.Buffer
	RunProbeCLI(&o, &e, cli)
	if o.String() != "" || e.String() != "" {
		t.Errorf("PRB-7: stdout %q stderr %q, want both empty", o.String(), e.String())
	}
}
