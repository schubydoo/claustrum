package git

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// A git.info whose listing cannot start because the stat of its folder fails with an
// error that startNameRefuses reports answers the hooks refusal with the chdir error.
// On Windows that is ERROR_INVALID_NAME, which 87375cc7 answers so (measured on
// Windows, for a path through a junction to a volume GUID path). A name with `<` gives
// that error here. On Linux and macOS the test reports ENOTDIR so, for a path below a
// file of the repository. A missing path keeps the answer of no repository.
func TestStartErrorOfABadFolderNameRefuses(t *testing.T) {
	f := newNoRepoFixture(t)
	tFile := filepath.Join(f.top, "t.txt")
	dir := filepath.Join(f.top, "a<b")
	if runtime.GOOS != "windows" {
		old := startNameRefuses
		t.Cleanup(func() { startNameRefuses = old })
		startNameRefuses = func(err error) bool { return errors.Is(err, syscall.ENOTDIR) }
		dir = filepath.Join(tFile, "sub")
	}
	_, statErr := os.Stat(dir)
	var pe *os.PathError
	if !errors.As(statErr, &pe) || !startNameRefuses(statErr) {
		t.Fatalf("stat %s: %v, want an error that startNameRefuses reports", dir, statErr)
	}
	want := errFrame(t, hooksRefusalPrefix+(&os.PathError{Op: "chdir", Path: dir, Err: pe.Err}).Error())
	// As the first request of a daemon 87375cc7 makes the excludes read only, and
	// no call on a second request (measured on Windows).
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
