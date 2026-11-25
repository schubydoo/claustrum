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
	if got := f.frame(t, "git.info", map[string]any{"path": dir}); got != want {
		t.Errorf("git.info = %s\nwant %s", got, want)
	}
	if got := f.frame(t, "git.info", map[string]any{"path": f.m}); got != resultOf(notRepoInfo) {
		t.Errorf("git.info of a missing path = %s\nwant %s", got, resultOf(notRepoInfo))
	}
}
