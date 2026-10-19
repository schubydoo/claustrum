//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A remove of the folder with the oldest use that fails part way keeps what
// the daemon knows of the copies that stay. A later sync on that repository
// then removes the copy of a file that went, and makes again the copies that
// the remove took (claustrum's own, not measured). Here a folder `info` of
// mode 0500 keeps the copy of info/exclude, as an open file does on Windows.
func TestShadowRepoLimitKeepsCopiesAfterAFailedRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes a file in a folder of mode 0500")
	}
	lowerRepoLimit(t, 2)
	f := newRepoFolderFixture(t)
	exclude := filepath.Join(f.repo, ".git", "info", "exclude")
	writeFile(t, exclude, "secret\n", 0o644)
	_, gitB := configRepo(t)
	_, gitC := configRepo(t)
	use := func(dir string) string {
		t.Helper()
		p := f.sync(t, dir)
		f.h.releaseRepos([]string{filepath.Base(p)})
		return p
	}
	a := use(f.repo)
	sweptThere(t, filepath.Join(a, "info", "exclude"))
	use(filepath.Dir(gitB))
	info := filepath.Join(a, "info")
	if err := os.Chmod(info, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(info, 0o700) })
	use(filepath.Dir(gitC))
	sweptGone(t, filepath.Join(a, "HEAD"))
	if err := os.Chmod(info, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(exclude); err != nil {
		t.Fatal(err)
	}
	use(f.repo)
	sweptGone(t, filepath.Join(a, "info", "exclude"))
	sweptThere(t, filepath.Join(a, "HEAD"))
	sweptThere(t, filepath.Join(a, "config"))
}
