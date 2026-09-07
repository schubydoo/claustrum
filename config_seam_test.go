package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// seamValidSHA is a well-formed 40-hex git SHA-1, the only shape
// version-override accepts. Declared locally so this file stands alone.
const seamValidSHA = "7c2f88d13e5f269762dd4d463aa4eb3102214110"

// errNoExecutable is the sentinel the osExecutable stub returns, so the arms
// below assert on an error this test owns rather than on "some error".
var errNoExecutable = errors.New("seam: executable path unavailable")

// stubOsExecutable swaps the os.Executable seam for the rest of the test.
func stubOsExecutable(t *testing.T, exe string, err error) {
	t.Helper()
	old := osExecutable
	osExecutable = func() (string, error) { return exe, err }
	t.Cleanup(func() { osExecutable = old })
}

// executableDir's failure arm has two triggers — a non-nil error and an empty
// path — and both must yield ("", false). The third case is the control that
// proves the stub is wired at all: a resolvable path must produce its directory.
func TestExecutableDirUnresolvable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		exe     string
		err     error
		wantDir string
		wantOK  bool
	}{
		{"os.Executable errors", "/nowhere/claustrum", errNoExecutable, "", false},
		{"os.Executable returns an empty path", "", nil, "", false},
		// Both sides OS-native: filepath.Dir cleans `/` to `\` on Windows.
		{"resolvable path (control)", filepath.FromSlash("/opt/srv/claustrum"), nil, filepath.FromSlash("/opt/srv"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubOsExecutable(t, tc.exe, tc.err)
			dir, ok := executableDir()
			if dir != tc.wantDir || ok != tc.wantOK {
				t.Errorf("executableDir() = (%q, %v), want (%q, %v)", dir, ok, tc.wantDir, tc.wantOK)
			}
		})
	}
}

// loadConfig must fall back to a zero config — stock behaviour, every divergence
// off — when the executable path cannot be resolved. The pair of arms is what
// makes this discriminating: the SAME claustrum.conf, holding a version-override
// that is unmistakable in the result, is read when the path resolves and ignored
// when it does not.
//
// The working directory is moved into that same directory on purpose. Dropping
// loadConfig's !ok guard would hand loadConfigFrom the empty dir executableDir
// returns on failure, which resolves "claustrum.conf" against the cwd — so
// without the chdir the unresolvable arm would pass either way and assert
// nothing.
func TestLoadConfigFallsBackWhenExecutableUnresolvable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configFileName),
		[]byte("version-override = "+seamValidSHA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	exe := filepath.Join(dir, "claustrum")

	t.Run("unresolvable", func(t *testing.T) {
		stubOsExecutable(t, exe, errNoExecutable)
		if cfg := loadConfig(); cfg != (config{}) {
			t.Errorf("loadConfig() = %+v, want the zero config (stock behaviour)", cfg)
		}
	})
	t.Run("resolvable (control)", func(t *testing.T) {
		stubOsExecutable(t, exe, nil)
		if got := loadConfig().versionOverride; got != seamValidSHA {
			t.Errorf("loadConfig().versionOverride = %q, want %q — the stub never reached the file", got, seamValidSHA)
		}
	})
}
