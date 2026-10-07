//go:build unix

package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// installWorktreeIndex puts the index that the checkout wrote at src into adminDir,
// the registration of the new worktree, as the file "index". It makes a new file
// there and copies the bytes, so the file equals the one of 89cb6289 in these facts
// (Linux and macOS VMs, rows A1 to A13):
//
//   - It is a new inode, not the temporary file moved.
//   - Its group is the group that a new file gets in the registration (rows A1, A3,
//     A5, A6, A8 and A9).
//   - Its mode is 0666 less the umask of the daemon (row A12). It follows neither the
//     mode of the temporary file nor core.sharedRepository (row A13).
//   - Its mtime is the mtime of the temporary file, rounded up to a whole
//     microsecond. The atime is not set: no row measures it.
//
// A temporary index that is gone is a failed placement. The error reads "open
// <src>: no such file or directory", the text of cell X1 (Linux and macOS VMs).
//
// The file is opened through a root at the parent of adminDir, the registrations
// directory. A failure of that open then reads "openat <name>/index: <OS error>",
// the text of rows A14, A14f and A14b. A file that exists already at the index is
// removed first, so the index is a new inode with the mode above (cell X8, Linux
// and macOS VMs). In a registration without its write bit and with no index, that
// remove finds no file, so the open gives the text of row A14. A remove that fails
// for another reason fails the placement with its own error, "removeat
// <name>/index: <OS error>", as on 89cb6289 (cells Y3 and Y7, Linux VM). A failure
// after the open leaves the file as it is, and the rollback of the caller deletes
// the registration.
func installWorktreeIndex(src, adminDir string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	adminDir = filepath.Clean(adminDir)
	root, err := os.OpenRoot(filepath.Dir(adminDir))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(adminDir) + "/index"
	if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return root.Chtimes(name, time.Time{}, roundUpToMicrosecond(st.ModTime()))
}

// removeCreatedRegistration deletes adminDir, the registration that
// createdWorktreeAdminDir verified, through a root at its parent. The error then
// reads "RemoveAll <name>: <OS error>", the wording of row A14, and the delete never
// follows a symlink out of the registrations directory. An empty adminDir and a
// parent that does not exist have nothing to delete. It is step B of
// undoFailedCheckout.
func removeCreatedRegistration(adminDir string) error {
	if adminDir == "" {
		return nil
	}
	adminDir = filepath.Clean(adminDir)
	root, err := os.OpenRoot(filepath.Dir(adminDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = root.Close() }()
	return root.RemoveAll(filepath.Base(adminDir))
}

// roundUpToMicrosecond returns t when it is a whole microsecond, and the next whole
// microsecond otherwise. No row holds a temporary index with a whole microsecond
// mtime, so the first case is claustrum's choice (not measured).
func roundUpToMicrosecond(t time.Time) time.Time {
	if whole := t.Truncate(time.Microsecond); !whole.Equal(t) {
		return whole.Add(time.Microsecond)
	}
	return t
}
