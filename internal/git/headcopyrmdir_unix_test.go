//go:build unix

package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveEmptyHeadFolderOnlyRemovesAnEmptyFolder: the remove of the copy of
// HEAD is one rmdir. An empty folder goes. A regular file, a link and a folder
// with a file stay, as when a file is put there after the Lstat of the
// request. A remove that tries a plain unlink first deletes the file.
func TestRemoveEmptyHeadFolderOnlyRemovesAnEmptyFolder(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(t *testing.T, head string)
		gone bool
	}{
		{"an empty folder", func(t *testing.T, head string) {
			if err := os.Mkdir(head, 0o700); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"a regular file", func(t *testing.T, head string) { mustWrite(t, head, "ref: refs/heads/main\n") }, false},
		{"a link", func(t *testing.T, head string) {
			if err := os.Symlink(t.TempDir(), head); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a folder with a file", func(t *testing.T, head string) { mustWrite(t, filepath.Join(head, "kept"), "kept\n") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			head := filepath.Join(dir, "HEAD")
			tc.make(t, head)
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			err = removeEmptyHeadFolder(root, dir)
			_, statErr := os.Lstat(head)
			if gone := os.IsNotExist(statErr); gone != tc.gone || (err == nil) != tc.gone {
				t.Errorf("the remove = %v, HEAD gone %v, want gone %v", err, gone, tc.gone)
			}
		})
	}
}

// TestRemoveEmptyHeadFolderNeedsTheFolderOfTheRoot: a folder at the path that
// is not the folder of the root keeps its empty HEAD folder.
func TestRemoveEmptyHeadFolderNeedsTheFolderOfTheRoot(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(other, "HEAD"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := removeEmptyHeadFolder(root, other); err == nil {
		t.Error("the remove through another folder succeeded")
	}
	if _, err := os.Lstat(filepath.Join(other, "HEAD")); err != nil {
		t.Errorf("the HEAD folder of the other folder: %v, want it kept", err)
	}
}

// TestRemoveEmptyHeadFolderRefusesHome: a HEAD copy path that is the home
// folder is refused before the rmdir, and the folder stays.
func TestRemoveEmptyHeadFolderRefusesHome(t *testing.T) {
	dir := t.TempDir()
	head := filepath.Join(dir, "HEAD")
	if err := os.Mkdir(head, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", head)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := removeEmptyHeadFolder(root, dir); err == nil {
		t.Error("removeEmptyHeadFolder of the home folder = nil, want a refusal")
	}
	if fi, err := os.Lstat(head); err != nil || !fi.IsDir() {
		t.Errorf("the home folder at HEAD: %v, %v, want it kept", fi, err)
	}
}

// TestRmdirHeadCopyFailsWithNoFolder: a closed root and a folder that is gone
// from its path give an error, and no rmdir runs.
func TestRmdirHeadCopyFailsWithNoFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(dir, "HEAD"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	moved := dir + ".moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := rmdirHeadCopy(root, dir); err == nil {
		t.Error("rmdirHeadCopy with the folder gone from its path = nil, want an error")
	}
	_ = root.Close()
	if err := rmdirHeadCopy(root, moved); err == nil {
		t.Error("rmdirHeadCopy with a closed root = nil, want an error")
	}
	if fi, err := os.Lstat(filepath.Join(moved, "HEAD")); err != nil || !fi.IsDir() {
		t.Errorf("HEAD: %v, %v, want the empty folder kept", fi, err)
	}
}
