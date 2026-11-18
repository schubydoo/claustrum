package git

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestShadowFolderFileIsMadeAFolderAgain: an empty file at the folder of the
// daemon goes, and a folder of the daemon is there again. git.info, git.status,
// git.list_branches and git.worktree_create succeed, as on 4c111e86 (measured on
// Linux).
func TestShadowFolderFileIsMadeAFolderAgain(t *testing.T) {
	f := newShadowFixture(t)
	f.info(t, f.repo, nil)
	first := daemonFolder(t, f.run)
	f.d.Shadow.mu.Lock()
	f.d.Shadow.dropLock()
	f.d.Shadow.mu.Unlock()
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.fiveRequests(nil)[:4] {
		if raw, _ := f.call(t, m.method, m.params); strings.Contains(raw, `"error"`) {
			t.Errorf("%s after the file took the folder = %s", m.method, raw)
		}
	}
	if _, err := os.Lstat(first); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the file at the old folder: %v, want it gone", err)
	}
	dir := daemonFolder(t, f.run)
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("the folder of the daemon: %v, %v, want a folder", fi, err)
	}
	if got := entryNames(t, dir); !slices.Contains(got, shadowLockName) || !slices.Contains(got, shadowRepoName(f.gitDir)) {
		t.Errorf("the new folder holds %v, want the lock and the folder of the repository", got)
	}
}

// TestRemoveOwnFolderFile: the remove takes only a regular file
// at a name in git-shadow. A folder, a path outside git-shadow
// and a path below a folder of git-shadow stay.
func TestRemoveOwnFolderFile(t *testing.T) {
	run := resolveTestRoot(t, t.TempDir())
	h := NewShadowHost(run)
	base := filepath.Join(run, shadowRootName)
	folder := filepath.Join(base, "folder")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	mustWrite(t, file, "")
	if err := h.removeOwnFolderFile(file); err != nil {
		t.Errorf("remove of a file: %v", err)
	}
	if _, err := os.Lstat(file); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the file: %v, want it gone", err)
	}
	if err := h.removeOwnFolderFile(folder); err == nil {
		t.Error("remove of a folder: nil, want an error")
	}
	if _, err := os.Lstat(folder); err != nil {
		t.Errorf("the folder: %v, want it kept", err)
	}
	// A path one folder below git-shadow names its last part in the root
	// without the folder test.
	twin := filepath.Join(base, "twin")
	mustWrite(t, twin, "")
	if err := h.removeOwnFolderFile(filepath.Join(base, "sub", "twin")); err == nil {
		t.Error("remove of a path below a folder of git-shadow: nil, want an error")
	}
	if _, err := os.Lstat(twin); err != nil {
		t.Errorf("the file of that name in git-shadow: %v, want it kept", err)
	}
	outside := filepath.Join(run, "outside")
	mustWrite(t, outside, "")
	if err := h.removeOwnFolderFile(outside); err == nil {
		t.Error("remove of a file outside git-shadow: nil, want an error")
	}
	if _, err := os.Lstat(outside); err != nil {
		t.Errorf("the file outside git-shadow: %v, want it kept", err)
	}
}
