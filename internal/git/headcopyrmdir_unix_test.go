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
