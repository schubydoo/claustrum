//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The temp-folder rules where a test of the temp dir fails with EACCES (4c111e86,
// measured on macOS; claustrum's own on Linux). Mode bits give the same
// errno on Linux and macOS, so these tests run on both as an account that is not root.

// runTempRootBoth runs the step with the temp dir x in TMPDIR and CLAUDE_CODE_TMPDIR,
// which names it on Linux and on macOS.
func runTempRootBoth(t *testing.T, x, runDir string, releases *[]func()) ([]string, []byte) {
	t.Helper()
	in := []string{"TMPDIR=" + x, "CLAUDE_CODE_TMPDIR=" + x}
	env, res, release, _ := privateTempRoot(in, runDir)
	*releases = append(*releases, release)
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return env, b
}

func skipAsRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root passes the mode bits")
	}
}

// A temp dir whose attributes cannot be read gives no member and no line. The
// failed stat here comes from a folder above it that cannot be searched, which is
// claustrum's own case, not measured.
func TestCLITempRootTempDirStatDenied(t *testing.T) {
	skipAsRoot(t)
	var rel []func()
	cleanTempRoots(t, &rel)
	base, runDir := tempRootDir(t, false)
	x := filepath.Join(base, "in")
	if err := os.Mkdir(x, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o700) })
	logs := captureLogBuf(t)
	env, b := runTempRootBoth(t, x, runDir, &rel)
	if string(b) != "null" {
		t.Errorf("answer %s, want no member", b)
	}
	if !slices.Equal(env, []string{"TMPDIR=" + x, "CLAUDE_CODE_TMPDIR=" + x}) {
		t.Errorf("env %q, want it as it was", env)
	}
	if strings.Contains(logs.String(), "temp folder") {
		t.Errorf("log %q, want no line", logs.String())
	}
}

// A temp dir that can be read but not searched gives "out of reach" and the text
// of a link or a file, with the refuse line.
func TestCLITempRootTempDirOutOfReach(t *testing.T) {
	skipAsRoot(t)
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, true)
	if err := os.Chmod(x, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(x, 0o700) })
	logs := captureLogBuf(t)
	env, b := runTempRootBoth(t, x, runDir, &rel)
	if want := `{"refused":"out of reach","whyNot":"only a folder of root's or of this account's own that others cannot write is stepped around: another account may have laid this"}`; string(b) != want {
		t.Fatalf("answer %s, want %s", b, want)
	}
	if v := tempEnvValue(env, "CLAUDE_CODE_TMPDIR"); v != x {
		t.Errorf("CLAUDE_CODE_TMPDIR %q, want %q", v, x)
	}
	if want := "[process.Manager] Claude Code's temp folder is unfit (it is out of reach), but no other was made: " + tempRootSteppedAround + "\n"; !strings.Contains(logs.String(), want) {
		t.Errorf("log %q, want %q", logs.String(), want)
	}
}

// A temp dir where no folder can be made gives the text of the failed mkdir, with
// the refuse line.
func TestCLITempRootTempDirNoMkdir(t *testing.T) {
	skipAsRoot(t)
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, true)
	if err := os.Chmod(x, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(x, 0o700) })
	logs := captureLogBuf(t)
	env, b := runTempRootBoth(t, x, runDir, &rel)
	if want := `{"refused":"` + unfitKind + `","whyNot":"no folder could be made in the temp dir: permission denied"}`; string(b) != want {
		t.Fatalf("answer %s, want %s", b, want)
	}
	if v := tempEnvValue(env, "CLAUDE_CODE_TMPDIR"); v != x || len(ccdFolders(t, x)) != 0 {
		t.Errorf("CLAUDE_CODE_TMPDIR %q, want %q and no new folder", v, x)
	}
	if want := "[process.Manager] Claude Code's temp folder is unfit (it is " + unfitKind + "), but no other was made: no folder could be made in the temp dir: permission denied\n"; !strings.Contains(logs.String(), want) {
		t.Errorf("log %q, want %q", logs.String(), want)
	}
}
