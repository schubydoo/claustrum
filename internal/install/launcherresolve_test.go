//go:build !windows

package install

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHostPolicySeen pins hostPolicySeen (c9125eb8, measured on Linux and macOS): true
// for a settings file or a drop-in with a key other than processWrapper, false for
// processWrapper only, {}, no file and a file that is not JSON.
func TestHostPolicySeen(t *testing.T) {
	old := managedSystemSettingsDir
	managedSystemSettingsDir = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { managedSystemSettingsDir = old })
	cases := []struct {
		name   string
		base   string
		dropIn string
		want   bool
	}{
		{"no file", "", "", false},
		{"wrapper only", `{"processWrapper":"/x"}`, "", false},
		{"wrapper and a key", `{"processWrapper":"/x","x":1}`, "", true},
		{"a key only", `{"x":1}`, "", true},
		{"sshHostLauncher", `{"processWrapper":"/x","sshHostLauncher":{"sha256":[]}}`, "", true},
		{"drop-in with a key", `{"processWrapper":"/x"}`, `{"y":2}`, true},
		{"drop-in wrapper only", "", `{"processWrapper":"/x"}`, false},
		{"empty object", `{}`, "", false},
		{"not JSON", `{`, "", false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		t.Setenv(managedSettingsDirEnv, dir)
		if c.base != "" {
			if err := os.WriteFile(filepath.Join(dir, managedSettingsBase), []byte(c.base), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if c.dropIn != "" {
			d := filepath.Join(dir, managedSettingsDropIns)
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, "10-a.json"), []byte(c.dropIn), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := HostPolicySeen(); got != c.want {
			t.Errorf("%s: hostPolicySeen = %v, want %v", c.name, got, c.want)
		}
	}
}
