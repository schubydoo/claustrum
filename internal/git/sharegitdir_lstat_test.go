package git

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestShareGitDirLstatError: on a share, the walk of shareGitDir goes on above a
// `.git` that does not exist (4c111e86, measured on Windows for a baseRepo one
// folder below the top folder), and ends with "" at a `.git` whose Lstat fails with
// another error, such as access denied (claustrum's own, not measured). The Lstat
// error is a fake, so the test runs on every system.
func TestShareGitDirLstatError(t *testing.T) {
	repo := t.TempDir()
	makeDir(t, filepath.Join(repo, ".git"))
	base := filepath.Join(repo, "sub")
	makeDir(t, base)
	oldShare, oldLstat := repoOnShare, lstatPath
	t.Cleanup(func() { repoOnShare, lstatPath = oldShare, oldLstat })
	repoOnShare = func(r string) bool { return r == base }
	rows := []struct {
		name string
		err  error
		want string
	}{
		{"does not exist", fs.ErrNotExist, filepath.Join(repo, ".git")},
		{"access denied", fs.ErrPermission, ""},
		{"another error", fs.ErrInvalid, ""},
	}
	for _, row := range rows {
		lstatPath = func(path string) (fs.FileInfo, error) {
			if path == filepath.Join(base, ".git") {
				return nil, &fs.PathError{Op: "lstat", Path: path, Err: row.err}
			}
			return os.Lstat(path)
		}
		if got := shareGitDir(base); got != row.want {
			t.Errorf("%s: shareGitDir = %q, want %q", row.name, got, row.want)
		}
	}
}
