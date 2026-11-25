package git

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// badStatNameDir is a folder whose stat fails with an error that startNameRefuses
// reports, and that error. On Windows a name with `<` gives ERROR_INVALID_NAME. On
// Linux and macOS startNameRefuses stands in with ENOTDIR, for a path below a file
// of the repository.
func badStatNameDir(t *testing.T, f noRepoFixture) (string, *os.PathError) {
	t.Helper()
	dir := filepath.Join(f.top, "a<b")
	if runtime.GOOS != "windows" {
		old := startNameRefuses
		t.Cleanup(func() { startNameRefuses = old })
		startNameRefuses = func(err error) bool { return errors.Is(err, syscall.ENOTDIR) }
		dir = filepath.Join(f.top, "t.txt", "sub")
	}
	_, statErr := os.Stat(dir)
	var pe *os.PathError
	if !errors.As(statErr, &pe) || !startNameRefuses(statErr) {
		t.Fatalf("stat %s: %v, want an error that startNameRefuses reports", dir, statErr)
	}
	return dir, pe
}

// A git.info on a folder whose stat fails with an error that startNameRefuses
// reports answers the hooks refusal with the chdir error.
// On Windows that is ERROR_INVALID_NAME, which 87375cc7 answers so (measured on
// Windows, for a path through a junction to a volume GUID path). A missing path
// keeps the answer of no repository.
func TestStartErrorOfABadFolderNameRefuses(t *testing.T) {
	f := newNoRepoFixture(t)
	dir, pe := badStatNameDir(t, f)
	want := errFrame(t, hooksRefusalPrefix+(&os.PathError{Op: "chdir", Path: dir, Err: pe.Err}).Error())
	// As the first request of a daemon 87375cc7 makes the excludes read only, and
	// after a git.info on a repository it makes none (measured on Windows). Here the
	// second call repeats the bad path (claustrum's own).
	for i, wantCalls := range []int{1, 0} {
		got, calls := f.call(t, "git.info", map[string]any{"path": dir})
		if got != want {
			t.Errorf("git.info %d = %s\nwant %s", i+1, got, want)
		}
		if len(calls) != wantCalls || (wantCalls == 1 && !calls[0].excludesRead()) {
			var argv [][]string
			for _, c := range calls {
				argv = append(argv, c.argv)
			}
			t.Errorf("git.info %d ran git %q, want %d call(s): the excludes read only", i+1, argv, wantCalls)
		}
	}
	if got := f.frame(t, "git.info", map[string]any{"path": f.m}); got != resultOf(notRepoInfo) {
		t.Errorf("git.info of a missing path = %s\nwant %s", got, resultOf(notRepoInfo))
	}
}

// The check of the daemon's own GIT_CONFIG_COUNT comes before the answer of a bad
// folder name, so such a folder with a refused count gets the count refusal
// (claustrum's own order, not measured).
func TestBadFolderNameKeepsTheCountRefusal(t *testing.T) {
	f := newNoRepoFixture(t)
	dir, _ := badStatNameDir(t, f)
	clearDaemonConfigEnv(t)
	t.Setenv(configCountName, "x")
	want := errFrame(t, hooksPinPrefix+`inherited GIT_CONFIG_COUNT "x" is not a count`)
	if got := f.frame(t, "git.info", map[string]any{"path": dir}); got != want {
		t.Errorf("git.info = %s\nwant %s", got, want)
	}
}

// GIT_ALTERNATE_OBJECT_DIRECTORIES in the daemon's environment refuses git.info
// after at most its excludes read (4c111e86, measured on Windows), so it comes
// before the answer of a bad folder name too (claustrum's own order, not
// measured). The daemon logs its line. This test runs the checks of Windows on
// every system.
func TestBadFolderNameKeepsTheAlternateEnvRefusal(t *testing.T) {
	f := newNoRepoFixture(t)
	dir, _ := badStatNameDir(t, f)
	checksOn(t)
	alternateEnvLogged.Store(false)
	t.Cleanup(func() { alternateEnvLogged.Store(false) })
	logs := captureLogBuf(t)
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(t.TempDir(), "objects"))
	got, calls := f.call(t, "git.info", map[string]any{"path": dir})
	if want := errFrame(t, alternateEnvText); got != want {
		t.Errorf("git.info = %s\nwant %s", got, want)
	}
	if len(calls) != 1 || !calls[0].excludesRead() {
		var argv [][]string
		for _, c := range calls {
			argv = append(argv, c.argv)
		}
		t.Errorf("git.info ran git %q, want 1 call: the excludes read only", argv)
	}
	if n := strings.Count(logs.String(), alternateEnvLine); n != 1 {
		t.Errorf("log has %d lines %q, want 1", n, alternateEnvLine)
	}
}
