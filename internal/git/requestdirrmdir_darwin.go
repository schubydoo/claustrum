package git

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// rmdirInFolder removes the empty folder name in folder by one rmdir by its
// path. macOS has no O_PATH, and a folder of mode 0300 cannot be opened. So
// an Lstat of folder must still give the identity parent right before the
// rmdir, or the step gets errParentSwapped. A short window stays between
// that test and the rmdir (claustrum's own).
func rmdirInFolder(folder, name string, parent os.FileInfo) error {
	now, err := os.Lstat(folder)
	if err != nil {
		return err
	}
	if !now.IsDir() || !os.SameFile(now, parent) {
		return errParentSwapped
	}
	return unix.Rmdir(filepath.Join(folder, name))
}
