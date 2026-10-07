//go:build unix

package main

import (
	"errors"
	"fmt"
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
// The file is opened through a root at the parent of adminDir with its symlinks
// resolved, the registrations directory. A failure of that open then reads "openat <name>/index: <OS error>",
// the text of rows A14, A14f and A14b. A file that exists already at the index is
// removed first, so the index is a new inode with the mode above (cell X8, Linux
// and macOS VMs). In a registration without its write bit and with no index, that
// remove finds no file, so the open gives the text of row A14. A remove that fails
// for another reason fails the placement with its own error, "removeat
// <name>/index: <OS error>", as on 89cb6289 (cells Y3 and Y7, Linux VM). A failure
// after the open leaves the file as it is, and the rollback of the caller deletes
// the registration. The remove takes a file, a link or an empty folder, never a
// tree. A file that another process makes between the remove and the open fails the
// open with "openat <name>/index: file exists" (not measured).
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
	// git writes a clean path, and that one is used as it is. A path that is not
	// clean is resolved first: a lexical clean drops a "link/.." pair and can name
	// another folder than the kernel does.
	if adminDir != filepath.Clean(adminDir) {
		adminDir, err = filepath.EvalSymlinks(adminDir)
		if err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(filepath.Dir(adminDir))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(adminDir) + "/index"
	if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// O_EXCL: a file that exists again after the remove is an error, and the open
	// does not follow a link that another process put there.
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
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

// removeCreatedRegistration deletes the registration that createdWorktreeAdminDir
// verified. It is step B of undoFailedCheckout. The delete acts on the resolved path
// that the check compared, never on a lexical clean of the raw path: a clean drops a
// "link/.." pair and can name another folder than the kernel does. The root is the
// resolved registrations directory, so the root is the containment from the open
// on, and the delete never follows a symlink out of it. The open itself follows a
// symlink that replaced the directory after the check. So the opened directory
// must have the identity that the check recorded (os.SameFile). If it differs,
// the delete is refused. The error of a failed delete reads "RemoveAll <name>: <OS
// error>", the wording of row A14.
//
// A registration that is not a direct child of the registrations directory is
// refused, and nothing is deleted. git writes each registration as a direct child.
// The zero value and a registrations directory that does not exist have nothing to
// delete.
func removeCreatedRegistration(reg createdRegistration) error {
	if reg.resolved == "" {
		return nil
	}
	name := filepath.Base(reg.resolved)
	if reg.resolved != filepath.Join(reg.registry, name) {
		return fmt.Errorf("%s is not a direct child of %s", reg.resolved, reg.registry)
	}
	root, err := os.OpenRoot(reg.registry)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = root.Close() }()
	if now, err := root.Stat("."); err != nil || !os.SameFile(reg.registryInfo, now) {
		return fmt.Errorf("%s is no longer the directory that was checked", reg.registry)
	}
	return root.RemoveAll(name)
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
